package app.pinky.companion

import android.hardware.display.DisplayManager
import android.os.Build
import android.os.SystemClock
import android.util.SparseIntArray
import android.view.Display
import android.view.accessibility.AccessibilityWindowInfo

/**
 * Which accessibility windows are on which display. Android 11 gives
 * accessibility services the windows of every display; Android 10 gives
 * them the main display's only, so requests for other displays fail there
 * with `unsupported`.
 */
class Displays(private val service: PinkyAccessibilityService) {

    /** Whether requests may name displays other than the main one. */
    val multiDisplay: Boolean
        get() = Build.VERSION.SDK_INT >= Build.VERSION_CODES.R

    /**
     * Throws unless requests may target the display: `bad_request` for a
     * negative id, `unsupported` for another display before Android 11, and
     * `not_found` for a display that does not exist (or no longer does).
     */
    fun check(display: Int) {
        if (display < 0) throw RequestError("bad_request", "display must be 0 or more, not $display")
        if (display == Display.DEFAULT_DISPLAY) return
        if (!multiDisplay) {
            throw RequestError(
                "unsupported",
                "display $display: Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT}) gives " +
                    "accessibility services the main display only; other displays need Android 11",
            )
        }
        if (service.getSystemService(DisplayManager::class.java)?.getDisplay(display) == null) {
            throw RequestError("not_found", "no display $display")
        }
    }

    /**
     * The windows on a display, in no particular order. Call [check] first.
     * Only call it on the accessibility worker thread; recycle the list with
     * TreeDumper.recycleWindows.
     */
    fun windowsOn(display: Int): List<AccessibilityWindowInfo> {
        if (display == Display.DEFAULT_DISPLAY || !multiDisplay) {
            return if (display == Display.DEFAULT_DISPLAY) service.windows else emptyList()
        }
        // A display that was just created may not be tracked yet.
        val deadline = SystemClock.uptimeMillis() + TRACK_WAIT_MS
        while (true) {
            val all = service.windowsOnAllDisplays
            var found: List<AccessibilityWindowInfo>? = null
            for (i in 0 until all.size()) {
                if (all.keyAt(i) == display) found = all.valueAt(i) else TreeDumper.recycleWindows(all.valueAt(i))
            }
            if (found != null) return found
            if (SystemClock.uptimeMillis() >= deadline) {
                throw RequestError(
                    "unsupported",
                    "display $display is not open to accessibility services " +
                        "(Android leaves out private virtual displays and overlay displays)",
                )
            }
            Thread.sleep(TRACK_POLL_MS)
        }
    }

    /**
     * The display of each app that has an application window now, leaving
     * out apps with windows on more than one display. Fetches each window's
     * root, so it runs on the worker thread, once when the service starts.
     */
    fun appDisplays(): Map<String, Int> {
        val found = HashMap<String, Int>()
        val both = HashSet<String>()
        fun add(display: Int, ws: List<AccessibilityWindowInfo>) {
            for (w in ws) {
                if (w.type != AccessibilityWindowInfo.TYPE_APPLICATION) continue
                val root = w.root ?: continue
                val pkg = root.packageName?.toString()
                TreeDumper.recycle(root)
                if (pkg.isNullOrEmpty()) continue
                val prev = found.put(pkg, display)
                if (prev != null && prev != display) both += pkg
            }
            TreeDumper.recycleWindows(ws)
        }
        if (multiDisplay) {
            val all = service.windowsOnAllDisplays
            for (i in 0 until all.size()) add(all.keyAt(i), all.valueAt(i))
        } else {
            add(Display.DEFAULT_DISPLAY, service.windows)
        }
        return found - both
    }

    /** Ids of the displays that have accessibility windows. Worker thread only. */
    fun list(): List<Int> {
        if (!multiDisplay) return listOf(Display.DEFAULT_DISPLAY)
        val all = service.windowsOnAllDisplays
        val ids = (0 until all.size()).map { all.keyAt(it) }.sorted()
        for (i in 0 until all.size()) TreeDumper.recycleWindows(all.valueAt(i))
        return ids
    }

    // Event attribution, on the main thread only: window id to display id.
    private val windowDisplays = SparseIntArray()
    private var stale = true
    private var refreshedAt = 0L

    /** The windows changed; the next lookup that misses reads them again. */
    fun windowsChanged() {
        stale = true
    }

    /**
     * The display a window is on, or Display.INVALID_DISPLAY when it is not
     * known. Before Android 11 only the main display's windows are known.
     */
    fun displayOfWindow(windowId: Int): Int {
        if (windowId < 0) return Display.INVALID_DISPLAY
        var d = windowDisplays.get(windowId, Display.INVALID_DISPLAY)
        if (d == Display.INVALID_DISPLAY &&
            (stale || SystemClock.uptimeMillis() - refreshedAt >= REFRESH_INTERVAL_MS)
        ) {
            refresh()
            d = windowDisplays.get(windowId, Display.INVALID_DISPLAY)
        }
        return d
    }

    private fun refresh() {
        windowDisplays.clear()
        try {
            if (multiDisplay) {
                val all = service.windowsOnAllDisplays
                for (i in 0 until all.size()) {
                    val ws = all.valueAt(i)
                    for (w in ws) windowDisplays.put(w.id, all.keyAt(i))
                    TreeDumper.recycleWindows(ws)
                }
            } else {
                val ws = service.windows
                for (w in ws) windowDisplays.put(w.id, Display.DEFAULT_DISPLAY)
                TreeDumper.recycleWindows(ws)
            }
        } catch (_: RuntimeException) {
            // The service is going away.
        }
        stale = false
        refreshedAt = SystemClock.uptimeMillis()
    }

    private companion object {
        const val TRACK_WAIT_MS = 300L
        const val TRACK_POLL_MS = 50L
        const val REFRESH_INTERVAL_MS = 100L
    }
}
