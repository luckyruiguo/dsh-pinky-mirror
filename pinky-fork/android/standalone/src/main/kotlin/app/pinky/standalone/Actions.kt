package app.pinky.standalone

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.graphics.Bitmap
import android.graphics.Path
import android.graphics.Point
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Base64
import android.view.Display
import android.view.accessibility.AccessibilityNodeInfo
import app.pinky.companion.Json
import app.pinky.companion.RequestError
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * The commands that act on the phone without adb. Each runs on the
 * connection's thread and replies once Android says it is done.
 */
class Actions(private val service: StandaloneService) {

    private val main = Handler(Looper.getMainLooper())
    private val shots = Executors.newSingleThreadExecutor()

    /**
     * gesture: strokes, each a list of points with a start and a duration,
     * all of one gesture: a tap is one point for 50 ms, a long press one
     * point for 800 ms, a swipe two points or more. Refused, with nothing
     * done, when a point lands on Pinky's own controls.
     */
    fun gesture(req: JSONObject): String {
        val display = req.optInt("display", Display.DEFAULT_DISPLAY)
        val strokes = req.getJSONArray("strokes")
        if (strokes.length() == 0 || strokes.length() > GestureDescription.getMaxStrokeCount()) {
            throw RequestError("bad_request", "a gesture has 1 to ${GestureDescription.getMaxStrokeCount()} strokes")
        }
        val b = GestureDescription.Builder()
        if (display != Display.DEFAULT_DISPLAY) {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
                throw RequestError("unsupported", "gestures on other displays need Android 11")
            }
            b.setDisplayId(display)
        }
        val points = ArrayList<Point>()
        var longest = 0L
        for (i in 0 until strokes.length()) {
            val s = strokes.getJSONObject(i)
            val pts = s.getJSONArray("points")
            if (pts.length() == 0) throw RequestError("bad_request", "a stroke needs a point")
            val path = Path()
            for (j in 0 until pts.length()) {
                val p = pts.getJSONArray(j)
                val x = p.getDouble(0).toFloat()
                val y = p.getDouble(1).toFloat()
                if (x < 0 || y < 0) throw RequestError("bad_request", "point ($x, $y) is off the screen")
                if (j == 0) path.moveTo(x, y) else path.lineTo(x, y)
                points += Point(x.toInt(), y.toInt())
            }
            val start = s.optLong("start_ms", 0).coerceAtLeast(0)
            val dur = s.optLong("duration_ms", TAP_MS).coerceIn(1, GestureDescription.getMaxGestureDuration())
            longest = maxOf(longest, start + dur)
            b.addStroke(GestureDescription.StrokeDescription(path, start, dur))
        }
        service.ownUI.check(points, display)

        val t0 = SystemClock.uptimeMillis()
        val done = CountDownLatch(1)
        var completed = false
        val sent = service.dispatchGesture(b.build(), object : AccessibilityService.GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) {
                completed = true
                done.countDown()
            }

            override fun onCancelled(gestureDescription: GestureDescription?) {
                done.countDown()
            }
        }, main)
        if (!sent) throw RequestError("action_failed", "Android refused the gesture")
        if (!done.await(longest + GESTURE_SLACK_MS, TimeUnit.MILLISECONDS)) {
            throw RequestError("timeout", "the gesture did not finish")
        }
        if (!completed) {
            throw RequestError("cancelled", "the gesture was cancelled, for example by a touch on the screen")
        }
        val j = Json()
        j.beginObject()
        j.field("took_ms", SystemClock.uptimeMillis() - t0)
        j.endObject()
        return j.toString()
    }

    /**
     * global: back, home, recents, notifications, or enter, which is the
     * keyboard's action key of the focused field (Android 11+). Never power
     * or lock: the prototype has no such action.
     */
    fun global(req: JSONObject): String {
        val name = req.getString("action")
        val t0 = SystemClock.uptimeMillis()
        val ok = when (name) {
            "back" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK)
            "home" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME)
            "recents" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_RECENTS)
            "notifications" -> service.performGlobalAction(AccessibilityService.GLOBAL_ACTION_NOTIFICATIONS)
            "enter" -> imeEnter()
            else -> throw RequestError("bad_request", "unknown global action: $name")
        }
        if (!ok) throw RequestError("action_failed", "Android refused $name")
        val j = Json()
        j.beginObject()
        j.field("took_ms", SystemClock.uptimeMillis() - t0)
        j.endObject()
        return j.toString()
    }

    private fun imeEnter(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            throw RequestError("unsupported", "the keyboard's action key needs Android 11")
        }
        val node = service.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: throw RequestError("not_found", "no field has the keyboard")
        if (node.packageName?.toString() == service.packageName) {
            throw RequestError("own_ui", OwnUI.REFUSAL)
        }
        return node.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_IME_ENTER.id)
    }

    /**
     * screenshot: the display as JPEG (default, quality 80) or PNG, in
     * base64. Android 11 and later; Android allows one every 333 ms, so a
     * request that comes sooner waits.
     */
    fun screenshot(req: JSONObject): String {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            throw RequestError("unsupported", "screenshots without adb need Android 11")
        }
        val display = req.optInt("display", Display.DEFAULT_DISPLAY)
        val png = req.optString("format", "jpeg") == "png"
        val quality = req.optInt("quality", 80).coerceIn(1, 100)
        val t0 = SystemClock.uptimeMillis()
        for (attempt in 1..4) {
            val done = CountDownLatch(1)
            var result: AccessibilityService.ScreenshotResult? = null
            var code = 0
            service.takeScreenshot(display, shots, object : AccessibilityService.TakeScreenshotCallback {
                override fun onSuccess(r: AccessibilityService.ScreenshotResult) {
                    result = r
                    done.countDown()
                }

                override fun onFailure(errorCode: Int) {
                    code = errorCode
                    done.countDown()
                }
            })
            if (!done.await(SHOT_TIMEOUT_MS, TimeUnit.MILLISECONDS)) {
                throw RequestError("timeout", "the screenshot did not come")
            }
            val r = result
            if (r != null) {
                val captured = SystemClock.uptimeMillis()
                val hw = Bitmap.wrapHardwareBuffer(r.hardwareBuffer, r.colorSpace)
                    ?: throw RequestError("action_failed", "the screenshot could not be read")
                val bmp = hw.copy(Bitmap.Config.ARGB_8888, false)
                hw.recycle()
                r.hardwareBuffer.close()
                val out = ByteArrayOutputStream(512 shl 10)
                bmp.compress(if (png) Bitmap.CompressFormat.PNG else Bitmap.CompressFormat.JPEG, quality, out)
                val w = bmp.width
                val h = bmp.height
                bmp.recycle()
                val encoded = SystemClock.uptimeMillis()
                val j = Json(out.size() * 4 / 3 + 256)
                j.beginObject()
                j.field("width", w)
                j.field("height", h)
                j.field("format", if (png) "png" else "jpeg")
                j.field("capture_ms", captured - t0)
                j.field("encode_ms", encoded - captured)
                j.field("bytes", out.size())
                j.field("data", Base64.encodeToString(out.toByteArray(), Base64.NO_WRAP))
                j.endObject()
                return j.toString()
            }
            when (code) {
                AccessibilityService.ERROR_TAKE_SCREENSHOT_INTERVAL_TIME_SHORT -> Thread.sleep(SHOT_INTERVAL_MS)
                ERROR_SECURE_WINDOW -> throw RequestError("secure", "an app on the screen forbids screenshots")
                else -> throw RequestError("action_failed", "Android could not take the screenshot (error $code)")
            }
        }
        throw RequestError("busy", "Android kept refusing screenshots as too frequent")
    }

    /** launch: opens an app by package name, as its launcher icon does. */
    fun launch(req: JSONObject): String {
        val pkg = req.getString("package")
        if (pkg == service.packageName) throw RequestError("own_ui", OwnUI.REFUSAL)
        val intent = service.packageManager.getLaunchIntentForPackage(pkg)
            ?: throw RequestError("not_found", "$pkg is not installed or has no launcher icon")
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_RESET_TASK_IF_NEEDED)
        val t0 = SystemClock.uptimeMillis()
        service.startActivity(intent)
        val j = Json()
        j.beginObject()
        j.field("package", pkg)
        j.field("took_ms", SystemClock.uptimeMillis() - t0)
        j.endObject()
        return j.toString()
    }

    /** apps: the launchable apps, with their names. */
    fun apps(): String {
        val pm = service.packageManager
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        val seen = HashSet<String>()
        val j = Json(16 shl 10)
        j.beginObject()
        j.name("apps").beginArray()
        for (ri in pm.queryIntentActivities(intent, 0)) {
            val ai = ri.activityInfo?.applicationInfo ?: continue
            if (!seen.add(ai.packageName)) continue
            j.beginObject()
            j.field("package", ai.packageName)
            j.field("label", ri.loadLabel(pm)?.toString().orEmpty())
            j.flag("system", ai.flags and ApplicationInfo.FLAG_SYSTEM != 0)
            j.endObject()
        }
        j.endArray()
        j.endObject()
        return j.toString()
    }

    companion object {
        const val TAP_MS = 50L
        private const val GESTURE_SLACK_MS = 5_000L
        private const val SHOT_TIMEOUT_MS = 5_000L
        private const val SHOT_INTERVAL_MS = 340L

        // AccessibilityService.ERROR_TAKE_SCREENSHOT_SECURE_WINDOW, Android 14.
        private const val ERROR_SECURE_WINDOW = 5
    }
}
