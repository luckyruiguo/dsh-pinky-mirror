package app.pinky.standalone

import android.content.Context
import android.util.Log
import app.pinky.companion.BuildConfig

/**
 * Pinky's Go core (internal/standalone/libcore), a shared library in this
 * process: the gateway with its rules and approvals, the MCP tools and the
 * agent. It drives the phone through this app's own companion socket and
 * serves Pinky's screens on CORE_SOCKET. Started once per process.
 */
object Core {
    private const val TAG = "PinkyCore"

    @Volatile
    private var started = false

    @Volatile
    var error: String? = null
        private set

    @Synchronized
    fun start(context: Context) {
        if (started) return
        started = true
        val err = try {
            System.loadLibrary("pinkycore")
            nativeStart(context.filesDir.absolutePath, BuildConfig.SOCKET_NAME, BuildConfig.CORE_SOCKET)
        } catch (e: UnsatisfiedLinkError) {
            "the core library is missing: $e"
        }
        if (err.isNotEmpty()) {
            error = err
            Log.e(TAG, "core did not start: $err")
        } else {
            Log.i(TAG, "core started")
        }
    }

    /** Starts the core; returns an error message, empty when it started. */
    @JvmStatic
    private external fun nativeStart(dataDir: String, companionSocket: String, coreSocket: String): String
}
