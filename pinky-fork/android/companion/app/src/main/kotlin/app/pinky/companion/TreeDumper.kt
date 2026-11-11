package app.pinky.companion

import android.app.KeyguardManager
import android.graphics.Rect
import android.graphics.Region
import android.graphics.RegionIterator
import android.hardware.display.DisplayManager
import android.os.Build
import android.os.SystemClock
import android.util.DisplayMetrics
import android.view.Display
import android.view.accessibility.AccessibilityNodeInfo
import android.view.accessibility.AccessibilityWindowInfo

/**
 * Serializes the windows of one display and their node trees to JSON. Only
 * call it on the accessibility worker thread.
 */
class TreeDumper(private val service: PinkyAccessibilityService) {

    class Options(
        val display: Int = Display.DEFAULT_DISPLAY,
        /** Walk the keyboard's nodes too. Off by default: keys are noise for agents. */
        val ime: Boolean = false,
        /** Keep nodes that are not visible to the user. */
        val invisible: Boolean = false,
        /** Walk the status and navigation bars too. */
        val bars: Boolean = false,
        val maxNodes: Int = 20_000,
    )

    private class State(val opt: Options, val width: Int, val height: Int) {
        var nodes = 0
        var truncated = false
        val rect = Rect()
    }

    fun dump(opt: Options): String {
        val started = SystemClock.elapsedRealtimeNanos()
        service.displays.check(opt.display)
        val display = service.getSystemService(DisplayManager::class.java)?.getDisplay(opt.display)
        val metrics = DisplayMetrics()
        @Suppress("DEPRECATION")
        display?.getRealMetrics(metrics)
        val st = State(opt, metrics.widthPixels, metrics.heightPixels)
        val j = Json(64 * 1024)
        j.beginObject()
        j.name("display").beginObject()
        j.field("id", opt.display)
        if (display != null) {
            j.field("width", metrics.widthPixels)
            j.field("height", metrics.heightPixels)
            j.field("density_dpi", metrics.densityDpi)
            j.field("rotation", display.rotation * 90)
        }
        j.endObject()
        // The phone's keyguard, which covers the main display only.
        val keyguard = service.getSystemService(KeyguardManager::class.java)
        j.field("locked", keyguard?.isKeyguardLocked ?: false)
        j.field("secure_locked", keyguard?.isDeviceLocked ?: false)

        val t0 = SystemClock.elapsedRealtimeNanos()
        val windows = service.displays.windowsOn(opt.display).sortedByDescending { it.layer }
        val windowsMs = (SystemClock.elapsedRealtimeNanos() - t0) / 1_000_000
        j.name("windows").beginArray()
        for (w in windows) {
            writeWindow(j, w, st)
        }
        j.endArray()
        recycleWindows(windows)

        j.field("windows_ms", windowsMs)
        j.field("nodes", st.nodes)
        j.flag("truncated", st.truncated)
        j.field("time", System.currentTimeMillis())
        j.field("took_ms", (SystemClock.elapsedRealtimeNanos() - started) / 1_000_000)
        j.endObject()
        return j.toString()
    }

    private fun writeWindow(j: Json, w: AccessibilityWindowInfo, st: State) {
        val type = w.type
        j.beginObject()
        j.field("id", w.id)
        j.field("type", windowType(type))
        j.field("layer", w.layer)
        j.fieldIfNotEmpty("title", w.title)
        w.getBoundsInScreen(st.rect)
        j.rect("bounds", st.rect.left, st.rect.top, st.rect.right, st.rect.bottom)
        val region = regionRects(w, st.rect)
        if (region != null) {
            j.name("region").beginArray()
            for (r in region) j.beginArray().value(r.left).value(r.top).value(r.right).value(r.bottom).endArray()
            j.endArray()
        }
        j.flag("active", w.isActive)
        j.flag("focused", w.isFocused)
        j.flag("pip", w.isInPictureInPictureMode)

        val walk = (type != AccessibilityWindowInfo.TYPE_INPUT_METHOD || st.opt.ime || isImeSheet(w, region, st)) &&
            (st.opt.bars || !isSystemBar(w, st))
        val t0 = SystemClock.elapsedRealtimeNanos()
        val root = if (walk) rootOf(w) else null
        val t1 = SystemClock.elapsedRealtimeNanos()
        val before = st.nodes
        if (root != null) {
            val pkg = root.packageName?.toString()
            j.fieldIfNotEmpty("package", pkg)
            j.fieldIfNotEmpty("app", pkg?.let { service.labels.get(it) })
            if (root.isVisibleToUser || st.opt.invisible) {
                j.name("root")
                writeNode(j, root, 0, st)
            }
            recycle(root)
        }
        // Where the time goes: the root fetch is a round trip to the app;
        // the walk mostly reads the cache that fetch prefetched into.
        j.field("root_ms", (t1 - t0) / 1_000_000)
        j.field("walk_ms", (SystemClock.elapsedRealtimeNanos() - t1) / 1_000_000)
        j.field("nodes", st.nodes - before)
        j.endObject()
    }

    /**
     * The root of a window. AccessibilityWindowInfo.getRoot bypasses the
     * node cache and drops the descendants it prefetched, so every child
     * then costs another round trip to the app. The active window's root
     * comes through the cache instead, and its prefetched descendants stay.
     */
    private fun rootOf(w: AccessibilityWindowInfo): AccessibilityNodeInfo? {
        if (w.isActive) {
            val r = service.rootInActiveWindow
            if (r != null && r.windowId == w.id) return r
            r?.let { recycle(it) }
        }
        return w.root
    }

    /**
     * The window's touchable region as rectangles, when it is more than the
     * one rectangle of its bounds (Android 13+). Gboard's stylus toolbar, for
     * one, takes a small strip on the left and one at the bottom, while its
     * bounds span most of the screen; Android hides from the user only the
     * nodes the region covers. Null when the region is the bounds, unknown,
     * or more than MAX_REGION_RECTS rectangles.
     */
    private fun regionRects(w: AccessibilityWindowInfo, bounds: Rect): List<Rect>? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return null
        val region = Region()
        w.getRegionInScreen(region)
        if (region.isEmpty || (region.isRect && region.bounds == bounds)) return null
        val rects = mutableListOf<Rect>()
        val it = RegionIterator(region)
        val r = Rect()
        while (it.next(r)) {
            if (rects.size == MAX_REGION_RECTS) return null
            rects += Rect(r)
        }
        return rects
    }

    /**
     * Whether an input method window covers the screen like a sheet rather
     * than sitting at the bottom as a keyboard: it reaches into the top third
     * of the display and its region covers at least half of it. Gboard's
     * stylus introduction does this, over a scrim. Android then reports the
     * app's nodes under it as not visible, so the input method has to be
     * walked or the tree would be empty.
     */
    private fun isImeSheet(w: AccessibilityWindowInfo, region: List<Rect>?, st: State): Boolean {
        if (w.type != AccessibilityWindowInfo.TYPE_INPUT_METHOD || st.width <= 0 || st.height <= 0) return false
        val r = st.rect
        w.getBoundsInScreen(r)
        val area = region?.sumOf { it.width().toLong() * it.height().toLong() } ?: (r.width().toLong() * r.height().toLong())
        return r.top < st.height / 3 && area * 2 >= st.width.toLong() * st.height.toLong()
    }

    /**
     * Whether a window is the status or navigation bar: a system window
     * spanning one edge of the screen that is at most 12% thick. The Go
     * client uses the same rule.
     */
    private fun isSystemBar(w: AccessibilityWindowInfo, st: State): Boolean {
        if (w.type != AccessibilityWindowInfo.TYPE_SYSTEM || st.width <= 0 || st.height <= 0) return false
        val r = st.rect
        w.getBoundsInScreen(r)
        val wide = r.width() >= st.width * 9 / 10
        val tall = r.height() >= st.height * 9 / 10
        return when {
            wide && r.height() <= st.height * 12 / 100 -> r.top <= 0 || r.bottom >= st.height
            tall && r.width() <= st.width * 12 / 100 -> r.left <= 0 || r.right >= st.width
            else -> false
        }
    }

    private fun writeNode(j: Json, n: AccessibilityNodeInfo, depth: Int, st: State) {
        st.nodes++
        j.beginObject()
        j.fieldIfNotEmpty("class", n.className)
        val password = n.isPassword
        val showingHint = n.isShowingHintText
        // Never report a password field's text: with "speak passwords" on,
        // some Android versions expose the real characters.
        if (!password && !showingHint) j.fieldIfNotEmpty("text", n.text)
        j.fieldIfNotEmpty("desc", n.contentDescription)
        j.fieldIfNotEmpty("hint", n.hintText)
        j.fieldIfNotEmpty("id", n.viewIdResourceName)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) j.fieldIfNotEmpty("state", n.stateDescription)
        roleDescription(n)?.let { j.fieldIfNotEmpty("role", it) }
        n.getBoundsInScreen(st.rect)
        j.rect("bounds", st.rect.left, st.rect.top, st.rect.right, st.rect.bottom)
        j.flag("clickable", n.isClickable)
        j.flag("long_clickable", n.isLongClickable)
        j.flag("checkable", n.isCheckable)
        @Suppress("DEPRECATION")
        j.flag("checked", n.isChecked)
        j.flag("editable", n.isEditable)
        j.flag("scrollable", n.isScrollable)
        j.flag("focusable", n.isFocusable)
        j.flag("focused", n.isFocused)
        j.flag("selected", n.isSelected)
        j.flag("password", password)
        j.flag("heading", n.isHeading)
        j.flag("disabled", !n.isEnabled)
        if (st.opt.invisible) j.flag("invisible", !n.isVisibleToUser)
        j.field("node", n.hashCode())

        val count = n.childCount
        if (count > 0) {
            if (depth >= MAX_DEPTH || st.nodes >= st.opt.maxNodes) {
                st.truncated = true
            } else {
                var open = false
                for (i in 0 until count) {
                    if (st.nodes >= st.opt.maxNodes) {
                        st.truncated = true
                        break
                    }
                    val c = n.getChild(i) ?: continue
                    if (c.isVisibleToUser || st.opt.invisible) {
                        if (!open) {
                            j.name("children").beginArray()
                            open = true
                        }
                        writeNode(j, c, depth + 1, st)
                    }
                    recycle(c)
                }
                if (open) j.endArray()
            }
        }
        j.endObject()
    }

    private fun roleDescription(n: AccessibilityNodeInfo): CharSequence? {
        val extras = n.extras
        if (extras.isEmpty) return null
        return extras.getCharSequence(ROLE_DESCRIPTION_KEY)
    }

    companion object {
        private const val MAX_DEPTH = 256
        private const val MAX_REGION_RECTS = 32

        // Set by AccessibilityNodeInfoCompat.setRoleDescription and Compose.
        private const val ROLE_DESCRIPTION_KEY = "AccessibilityNodeInfo.roleDescription"

        fun windowType(type: Int): String = when (type) {
            AccessibilityWindowInfo.TYPE_APPLICATION -> "application"
            AccessibilityWindowInfo.TYPE_INPUT_METHOD -> "input_method"
            AccessibilityWindowInfo.TYPE_SYSTEM -> "system"
            AccessibilityWindowInfo.TYPE_ACCESSIBILITY_OVERLAY -> "accessibility_overlay"
            AccessibilityWindowInfo.TYPE_SPLIT_SCREEN_DIVIDER -> "split_screen_divider"
            6 -> "magnification_overlay" // TYPE_MAGNIFICATION_OVERLAY, API 33
            else -> "unknown"
        }

        @Suppress("DEPRECATION")
        fun recycle(n: AccessibilityNodeInfo) {
            // A no-op from API 33; before that it returns the node to a pool.
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) n.recycle()
        }

        @Suppress("DEPRECATION")
        fun recycleWindows(ws: List<AccessibilityWindowInfo>) {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) ws.forEach { it.recycle() }
        }
    }
}
