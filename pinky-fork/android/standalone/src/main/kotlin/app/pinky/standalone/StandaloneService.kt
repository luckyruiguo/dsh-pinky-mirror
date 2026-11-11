package app.pinky.standalone

import android.content.Intent
import android.util.Log
import app.pinky.companion.PinkyAccessibilityService
import org.json.JSONObject

/**
 * The companion's accessibility service, kept on while the prototype is
 * installed, with commands that act on the phone: gestures, global actions,
 * screenshots and opening apps (Actions). It also hosts Pinky's Go core
 * (Core) and draws Pinky's own controls over other apps (Overlays).
 */
class StandaloneService : PinkyAccessibilityService() {

    val ownUI = OwnUI(this)
    private val actions = Actions(this)
    var overlays: Overlays? = null
        private set

    // The core drives the phone through this service, so it stays on.
    override val defaultIdleDisableMs: Long get() = 0L

    override fun onServiceConnected() {
        super.onServiceConnected()
        current = this
        Core.start(this)
        overlays?.stop()
        overlays = Overlays(this).also { it.start() }
        Log.i(TAG, "standalone service connected")
    }

    override fun onUnbind(intent: Intent?): Boolean {
        overlays?.stop()
        overlays = null
        if (current === this) current = null
        return super.onUnbind(intent)
    }

    override fun extraCommand(cmd: String, req: JSONObject): String? = when (cmd) {
        "gesture" -> actions.gesture(req)
        "global" -> actions.global(req)
        "screenshot" -> actions.screenshot(req)
        "launch" -> actions.launch(req)
        "apps" -> actions.apps()
        else -> null
    }

    override fun extraFeatures(): List<String> = listOf("gesture", "global", "screenshot", "launch", "apps", "own_ui")

    companion object {
        private const val TAG = "PinkyStandalone"

        @Volatile
        var current: StandaloneService? = null
            private set
    }
}
