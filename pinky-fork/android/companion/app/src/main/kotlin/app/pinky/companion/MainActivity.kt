package app.pinky.companion

import android.Manifest
import android.accessibilityservice.AccessibilityServiceInfo
import android.app.Activity
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Intent
import android.content.pm.PackageManager
import android.net.LocalSocket
import android.net.LocalSocketAddress
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.Editable
import android.text.TextWatcher
import android.view.accessibility.AccessibilityManager
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.util.concurrent.Executors

/**
 * The debug page: service status plus controls that Pinky's device tests
 * use. It runs in its own process (":debug"), so it reads the service's
 * status over the same socket the computer uses.
 *
 * `am start -n app.pinky.companion/.MainActivity --es toast <text>` shows a
 * toast and `--es notify <text>` posts a notification, so tests can produce
 * events without tapping.
 */
class MainActivity : Activity() {

    private lateinit var serviceView: TextView
    private lateinit var socketView: TextView
    private lateinit var echoView: TextView
    private lateinit var versionView: TextView

    private val main = Handler(Looper.getMainLooper())
    private val pinger = Executors.newSingleThreadExecutor()
    private var resumed = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        serviceView = findViewById(R.id.status_service)
        socketView = findViewById(R.id.status_socket)
        echoView = findViewById(R.id.debug_echo)
        versionView = findViewById(R.id.status_version)

        findViewById<EditText>(R.id.debug_input).addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
            override fun afterTextChanged(s: Editable?) = echo(s?.toString().orEmpty())
        })
        findViewById<Button>(R.id.debug_toast).setOnClickListener { showToast(getString(R.string.toast_default)) }
        findViewById<Button>(R.id.debug_notify).setOnClickListener {
            postNotification(getString(R.string.notification_default))
        }
        handleExtras(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleExtras(intent)
    }

    override fun onResume() {
        super.onResume()
        resumed = true
        refreshStatus()
    }

    override fun onPause() {
        resumed = false
        main.removeCallbacksAndMessages(null)
        super.onPause()
    }

    override fun onDestroy() {
        pinger.shutdownNow()
        super.onDestroy()
    }

    private fun handleExtras(i: Intent?) {
        if (i == null) return
        i.getStringExtra(EXTRA_TOAST)?.let { showToast(it) }
        i.getStringExtra(EXTRA_NOTIFY)?.let { postNotification(it) }
        i.removeExtra(EXTRA_TOAST)
        i.removeExtra(EXTRA_NOTIFY)
    }

    // Every few seconds while visible; views change only when the text does,
    // so the page stays quiet for settle detection.
    private fun refreshStatus() {
        val am = getSystemService(AccessibilityManager::class.java)
        val on = am?.getEnabledAccessibilityServiceList(AccessibilityServiceInfo.FEEDBACK_ALL_MASK)
            ?.any { it.resolveInfo.serviceInfo.packageName == packageName } == true
        setIfChanged(serviceView, getString(if (on) R.string.status_service_on else R.string.status_service_off))
        // Android can move the page to another display without recreating it.
        setIfChanged(
            versionView,
            getString(R.string.status_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE, displayId()),
        )
        pinger.execute {
            val ping = ping()
            main.post {
                val text = if (ping != null) {
                    getString(
                        R.string.status_socket_answering,
                        CompanionServer.SOCKET_NAME,
                        ping.optInt("protocol"),
                        ping.optInt("connections") - 1,
                    )
                } else {
                    getString(R.string.status_socket_closed, CompanionServer.SOCKET_NAME)
                }
                setIfChanged(socketView, text)
            }
        }
        if (resumed) main.postDelayed(::refreshStatus, STATUS_INTERVAL_MS)
    }

    private fun ping(): JSONObject? = try {
        LocalSocket().use { s ->
            s.connect(LocalSocketAddress(CompanionServer.SOCKET_NAME, LocalSocketAddress.Namespace.ABSTRACT))
            s.soTimeout = 2_000
            s.outputStream.write("{\"id\":1,\"cmd\":\"ping\"}\n".toByteArray())
            val line = BufferedReader(InputStreamReader(s.inputStream, Charsets.UTF_8)).readLine()
            val reply = JSONObject(line)
            if (reply.optBoolean("ok")) reply.getJSONObject("result") else null
        }
    } catch (_: Exception) {
        null
    }

    /** The display this page is on; tests open it on background displays too. */
    private fun displayId(): Int =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            display?.displayId ?: 0
        } else {
            @Suppress("DEPRECATION")
            windowManager.defaultDisplay.displayId
        }

    private fun setIfChanged(v: TextView, text: String) {
        if (v.text.toString() != text) v.text = text
    }

    private fun echo(text: String) {
        echoView.text = if (text.isEmpty()) {
            getString(R.string.echo_empty)
        } else {
            getString(R.string.echo_format, text.codePointCount(0, text.length), text)
        }
    }

    private fun showToast(text: String) {
        Toast.makeText(this, text, Toast.LENGTH_SHORT).show()
    }

    private fun postNotification(text: String) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            showToast(getString(R.string.notification_denied))
            return
        }
        val nm = getSystemService(NotificationManager::class.java) ?: return
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, getString(R.string.notification_channel), NotificationManager.IMPORTANCE_DEFAULT),
        )
        val n = Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_pinky)
            .setContentTitle(getString(R.string.notification_title))
            .setContentText(text)
            .setAutoCancel(true)
            .setTimeoutAfter(NOTIFICATION_TIMEOUT_MS)
            .build()
        nm.notify(NOTIFICATION_ID, n)
    }

    companion object {
        const val EXTRA_TOAST = "toast"
        const val EXTRA_NOTIFY = "notify"
        private const val CHANNEL_ID = "pinky_test"
        private const val NOTIFICATION_ID = 1
        private const val NOTIFICATION_TIMEOUT_MS = 15_000L
        private const val STATUS_INTERVAL_MS = 3_000L
    }
}
