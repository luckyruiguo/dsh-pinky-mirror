package app.pinky.standalone

import android.graphics.Point
import android.graphics.Rect
import android.graphics.Region
import android.os.Build
import android.view.accessibility.AccessibilityWindowInfo
import app.pinky.companion.RequestError
import app.pinky.companion.TreeDumper

/**
 * Keeps the agent's input off Pinky's own controls: the approval card and
 * the Working pill drawn over other apps, and the prototype's own screen.
 * The agent can never approve itself or press Stop for the person.
 *
 * This is one of several walls (docs/notes/standalone-phone.md, "Prototype
 * results"): the gateway refuses every action while Pinky's screen is in
 * front, and the controls are marked accessibilityDataSensitive, so they
 * are hidden from this service and, on Android 15 and later, drop the
 * touches it injects.
 */
class OwnUI(private val service: StandaloneService) {

    // What Overlays shows, in screen pixels of display 0.
    @Volatile
    private var overlays: List<Rect> = emptyList()

    fun setOverlays(rects: List<Rect>) {
        overlays = rects.map { Rect(it) }
    }

    fun overlayRects(): List<Rect> = overlays

    /** Throws when a point lands on Pinky's own controls; does nothing otherwise. */
    fun check(points: List<Point>, display: Int) {
        if (display == 0) {
            val o = overlays
            for (p in points) {
                if (o.any { it.contains(p.x, p.y) }) throw RequestError("own_ui", REFUSAL)
            }
        }
        val windows = try {
            when {
                display == 0 -> service.windows
                Build.VERSION.SDK_INT >= Build.VERSION_CODES.R -> service.windowsOnAllDisplays.get(display).orEmpty()
                else -> emptyList()
            }
        } catch (_: Exception) {
            emptyList()
        }
        try {
            // Top-most first: a touch goes to the first window under it.
            val sorted = windows.sortedByDescending { it.layer }
            for (p in points) {
                val w = sorted.firstOrNull { contains(it, p) } ?: continue
                if (ours(w)) throw RequestError("own_ui", REFUSAL)
            }
        } finally {
            TreeDumper.recycleWindows(windows)
        }
    }

    private fun contains(w: AccessibilityWindowInfo, p: Point): Boolean {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val r = Region()
            w.getRegionInScreen(r)
            return r.contains(p.x, p.y)
        }
        val b = Rect()
        w.getBoundsInScreen(b)
        return b.contains(p.x, p.y)
    }

    private fun ours(w: AccessibilityWindowInfo): Boolean {
        val root = w.root ?: return false
        return root.packageName?.toString() == service.packageName
    }

    companion object {
        const val REFUSAL =
            "the point is on Pinky's own controls (an approval, the Working pill or Pinky's screen); " +
                "agents can never press them"
    }
}
