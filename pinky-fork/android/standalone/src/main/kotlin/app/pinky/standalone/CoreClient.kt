package app.pinky.standalone

import android.net.LocalSocket
import android.net.LocalSocketAddress
import app.pinky.companion.BuildConfig
import org.json.JSONObject
import java.io.BufferedReader
import java.io.Closeable
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.util.concurrent.Executors

/**
 * Pinky's screens' side of the core's control socket
 * (internal/standalone/control.go): line JSON on an abstract socket that
 * only this app's uid may open. Every call blocks; run it off the main
 * thread.
 */
object CoreClient {

    private val io = Executors.newCachedThreadPool()

    private fun connect(): LocalSocket {
        val s = LocalSocket()
        s.connect(LocalSocketAddress(BuildConfig.CORE_SOCKET, LocalSocketAddress.Namespace.ABSTRACT))
        return s
    }

    /** Sends one command and returns its reply; throws when the core is not there or says no. */
    fun call(req: JSONObject): JSONObject {
        connect().use { s ->
            val w = OutputStreamWriter(s.outputStream, Charsets.UTF_8)
            w.write(req.toString())
            w.write("\n")
            w.flush()
            val line = BufferedReader(InputStreamReader(s.inputStream, Charsets.UTF_8)).readLine()
                ?: throw IllegalStateException("the core closed the connection")
            val r = JSONObject(line)
            if (!r.optBoolean("ok")) throw IllegalStateException(r.optString("error", "the core said no"))
            return r
        }
    }

    /** Runs call off the main thread; done gets the error message, or null. */
    fun send(req: JSONObject, done: (String?) -> Unit = {}) {
        io.execute {
            done(
                try {
                    call(req)
                    null
                } catch (e: Exception) {
                    e.message ?: e.toString()
                },
            )
        }
    }

    fun cmd(name: String, vararg fields: Pair<String, Any>): JSONObject {
        val j = JSONObject().put("cmd", name)
        for ((k, v) in fields) j.put(k, v)
        return j
    }

    /**
     * Follows the state: onState gets each state the core sends, on a
     * background thread, and onDown the reason while the core cannot be
     * reached (it is retried every second). Close the result to stop.
     */
    fun subscribe(onState: (JSONObject) -> Unit, onDown: (String) -> Unit): Closeable {
        val sub = Subscription(onState, onDown)
        io.execute(sub)
        return sub
    }

    private class Subscription(
        private val onState: (JSONObject) -> Unit,
        private val onDown: (String) -> Unit,
    ) : Runnable, Closeable {

        @Volatile
        private var closed = false

        @Volatile
        private var socket: LocalSocket? = null

        override fun run() {
            while (!closed) {
                try {
                    val s = connect()
                    socket = s
                    if (closed) break
                    val w = OutputStreamWriter(s.outputStream, Charsets.UTF_8)
                    w.write("{\"cmd\":\"subscribe\"}\n")
                    w.flush()
                    val r = BufferedReader(InputStreamReader(s.inputStream, Charsets.UTF_8))
                    while (!closed) {
                        val line = r.readLine() ?: break
                        val st = JSONObject(line).optJSONObject("state") ?: continue
                        onState(st)
                    }
                } catch (e: Exception) {
                    if (!closed) onDown(e.message ?: e.toString())
                } finally {
                    try {
                        socket?.close()
                    } catch (_: Exception) {
                    }
                    socket = null
                }
                if (!closed) Thread.sleep(1000)
            }
        }

        override fun close() {
            closed = true
            try {
                socket?.close()
            } catch (_: Exception) {
            }
        }
    }
}
