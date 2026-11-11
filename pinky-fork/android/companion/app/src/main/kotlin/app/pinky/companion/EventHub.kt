package app.pinky.companion

import android.app.Notification
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import android.view.Display
import android.view.accessibility.AccessibilityEvent
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/**
 * Turns accessibility events into protocol events and fans them out to
 * subscribed connections. Events arrive on the main thread; each subscriber
 * has its own queue and writer thread, so a slow client never blocks the
 * service. Every event names the display it came from when that is known.
 */
class EventHub(private val labels: AppLabels, private val displays: Displays) {

    /** Uptime of the last event that means the screen changed, on any display. */
    @Volatile
    var lastChangeUptime = 0L
        private set

    // The same per display; Display.INVALID_DISPLAY holds the changes whose
    // display is not known.
    private val lastChangeByDisplay = ConcurrentHashMap<Int, Long>()

    private val subscribers = CopyOnWriteArrayList<Subscriber>()
    private val main = Handler(Looper.getMainLooper())

    // Content changes come in bursts; send at most one per CONTENT_INTERVAL_MS
    // and display. Main thread only.
    private class ContentFold {
        var pending = 0
        var pkg: String? = null
        var lastTime = 0L
        var lastEmit = 0L
        var flushScheduled = false
    }

    private val content = HashMap<Int, ContentFold>()

    // The display and window of each package's last window event. Toast
    // events name neither a window nor a display (Android 16 reports -1 for
    // both), so a toast takes the display of its app's last window. Main
    // thread only.
    private val packageDisplays = HashMap<String, Int>()
    private val packageWindows = HashMap<String, Int>()

    /**
     * Records the displays of the apps that had windows when the service
     * started, so that a toast before any window event of its app still finds
     * its display. Window events seen meanwhile win. Main thread only.
     */
    fun seed(appDisplays: Map<String, Int>) {
        for ((pkg, d) in appDisplays) packageDisplays.putIfAbsent(pkg, d)
    }

    /** Subscribes a connection; a display limits it to that display's events. */
    fun subscribe(conn: Connection, kinds: Set<String>, display: Int?): Subscriber {
        val s = Subscriber(conn, kinds, display)
        subscribers.add(s)
        return s
    }

    fun unsubscribe(s: Subscriber) {
        subscribers.remove(s)
        s.close()
    }

    fun closeAll() {
        subscribers.forEach { it.close() }
        subscribers.clear()
    }

    /** Called on the main thread for every accessibility event. */
    fun onEvent(e: AccessibilityEvent) {
        when (e.eventType) {
            AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED -> {
                val d = displayOf(e)
                markChanged(d)
                val pkg = e.packageName?.toString()
                if (pkg != null) {
                    labels.warm(pkg)
                    packageDisplays[pkg] = d
                    packageWindows[pkg] = e.windowId
                }
                if (!wanted(KIND_WINDOW)) return
                val j = begin(KIND_WINDOW, e, d)
                j.fieldIfNotEmpty("class", e.className)
                j.fieldIfNotEmpty("title", joinText(e))
                j.field("window", e.windowId)
                emit(KIND_WINDOW, d, j)
            }

            AccessibilityEvent.TYPE_WINDOWS_CHANGED -> {
                // Look the window up before the list is read again, so that
                // a removed window is still found.
                val d = displayOf(e)
                displays.windowsChanged()
                markChanged(d)
                if (!wanted(KIND_WINDOWS)) return
                val j = begin(KIND_WINDOWS, e, d)
                j.field("window", e.windowId)
                j.name("changes").beginArray()
                windowChangeNames(e.windowChanges).forEach { j.value(it) }
                j.endArray()
                emit(KIND_WINDOWS, d, j)
            }

            AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED,
            AccessibilityEvent.TYPE_VIEW_SCROLLED,
            AccessibilityEvent.TYPE_VIEW_TEXT_CHANGED -> {
                val d = displayOf(e)
                markChanged(d)
                if (!wanted(KIND_CONTENT)) return
                onContent(e, d)
            }

            AccessibilityEvent.TYPE_NOTIFICATION_STATE_CHANGED -> {
                val data = e.parcelableData
                if (data is Notification) {
                    if (!wanted(KIND_NOTIFICATION)) return
                    // Notifications belong to the phone, not to a display.
                    val j = begin(KIND_NOTIFICATION, e, Display.INVALID_DISPLAY)
                    val extras = data.extras
                    j.fieldIfNotEmpty("title", extras?.getCharSequence(Notification.EXTRA_TITLE))
                    val body = extras?.getCharSequence(Notification.EXTRA_TEXT)
                        ?: extras?.getCharSequence(Notification.EXTRA_BIG_TEXT)
                    j.fieldIfNotEmpty("text", body ?: joinText(e))
                    j.fieldIfNotEmpty("category", data.category)
                    emit(KIND_NOTIFICATION, Display.INVALID_DISPLAY, j)
                } else {
                    // Toasts arrive as notification events with no Notification,
                    // also when SystemUI draws them for apps targeting API 30+.
                    if (!wanted(KIND_TOAST)) return
                    val d = toastDisplay(e)
                    val j = begin(KIND_TOAST, e, d)
                    j.field("text", toastText(e))
                    emit(KIND_TOAST, d, j)
                }
            }
        }
    }

    /**
     * Blocks until no change event arrived for quietMs, counted from the
     * call at the earliest, or until timeoutMs passed. With a display, only
     * that display's changes count, and those whose display is not known.
     */
    fun waitIdle(quietMs: Long, timeoutMs: Long, display: Int?): String {
        val start = SystemClock.uptimeMillis()
        var settled: Boolean
        while (true) {
            val now = SystemClock.uptimeMillis()
            val quietFor = now - maxOf(lastChange(display), start)
            if (quietFor >= quietMs) {
                settled = true
                break
            }
            if (now - start >= timeoutMs) {
                settled = false
                break
            }
            Thread.sleep(minOf(quietMs - quietFor, 25L).coerceAtLeast(1L))
        }
        val now = SystemClock.uptimeMillis()
        val last = lastChange(display)
        val j = Json()
        j.beginObject()
        j.field("settled", settled)
        j.field("waited_ms", now - start)
        if (last > 0) j.field("last_change_ms_ago", now - last)
        j.endObject()
        return j.toString()
    }

    private fun lastChange(display: Int?): Long = if (display == null) {
        lastChangeUptime
    } else {
        maxOf(lastChangeByDisplay[display] ?: 0L, lastChangeByDisplay[Display.INVALID_DISPLAY] ?: 0L)
    }

    /**
     * The display an event came from, or Display.INVALID_DISPLAY. Android
     * 13 reports it; before that the event's window tells.
     */
    private fun displayOf(e: AccessibilityEvent): Int {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val d = e.displayId
            if (d != Display.INVALID_DISPLAY) return d
        }
        return displays.displayOfWindow(e.windowId)
    }

    /**
     * A toast's display: the one Android reports, else that of the app's
     * last window event. When that event's display was not known (on Android
     * 11 and 12 a window can be listed only after its event), its window is
     * looked up again now.
     */
    private fun toastDisplay(e: AccessibilityEvent): Int {
        val d = displayOf(e)
        if (d != Display.INVALID_DISPLAY) return d
        val pkg = e.packageName?.toString() ?: return Display.INVALID_DISPLAY
        val last = packageDisplays[pkg] ?: return Display.INVALID_DISPLAY
        if (last != Display.INVALID_DISPLAY) return last
        return packageWindows[pkg]?.let { displays.displayOfWindow(it) } ?: Display.INVALID_DISPLAY
    }

    /**
     * The toast's message. SystemUI's toasts (Android 12+) also report the
     * app's name, which is dropped when the event has more than one text.
     */
    private fun toastText(e: AccessibilityEvent): String {
        val parts = e.text.mapNotNull { it?.toString()?.trim() }.filter { it.isNotEmpty() }
        val label = e.packageName?.let { labels.load(it.toString()) }
        val message = if (parts.size > 1 && label != null) parts.filter { it != label } else parts
        return message.joinToString(" ")
    }

    private fun markChanged(display: Int) {
        val now = SystemClock.uptimeMillis()
        lastChangeUptime = now
        lastChangeByDisplay[display] = now
    }

    private fun wanted(kind: String): Boolean = subscribers.any { kind in it.kinds }

    private fun onContent(e: AccessibilityEvent, display: Int) {
        val f = content.getOrPut(display) { ContentFold() }
        f.pending++
        f.pkg = e.packageName?.toString()
        f.lastTime = eventTime(e)
        val now = SystemClock.uptimeMillis()
        if (now - f.lastEmit >= CONTENT_INTERVAL_MS) {
            flushContent(display, f)
        } else if (!f.flushScheduled) {
            f.flushScheduled = true
            main.postAtTime({
                f.flushScheduled = false
                flushContent(display, f)
            }, f.lastEmit + CONTENT_INTERVAL_MS)
        }
    }

    private fun flushContent(display: Int, f: ContentFold) {
        if (f.pending == 0) return
        val j = Json()
        j.beginObject()
        j.field("event", KIND_CONTENT)
        j.field("time", f.lastTime)
        j.fieldIfNotEmpty("package", f.pkg)
        j.field("display", display)
        j.field("count", f.pending)
        f.pending = 0
        f.lastEmit = SystemClock.uptimeMillis()
        emit(KIND_CONTENT, display, j)
    }

    private fun begin(kind: String, e: AccessibilityEvent, display: Int): Json {
        val j = Json()
        j.beginObject()
        j.field("event", kind)
        j.field("time", eventTime(e))
        j.fieldIfNotEmpty("package", e.packageName)
        j.field("display", display)
        return j
    }

    private fun emit(kind: String, display: Int, j: Json) {
        j.endObject()
        val line = j.toString()
        for (s in subscribers) {
            if (s.accepts(kind, display)) s.offer(line)
        }
    }

    /** A connection that receives events, of one display or of all. */
    inner class Subscriber(private val conn: Connection, val kinds: Set<String>, private val display: Int?) {
        private val queue = LinkedBlockingQueue<String>(QUEUE_CAPACITY)
        private val dropped = AtomicInteger()

        @Volatile
        private var closed = false
        private var thread: Thread? = null

        /** Starts delivery. Called after the subscribe reply went out. */
        fun start() {
            thread = Thread(::loop, "pinky-events").apply {
                isDaemon = true
                start()
            }
        }

        /** Notifications belong to the phone and pass any display filter. */
        fun accepts(kind: String, d: Int): Boolean =
            kind in kinds && (display == null || kind == KIND_NOTIFICATION || d == display)

        fun offer(line: String) {
            if (!closed && !queue.offer(line)) dropped.incrementAndGet()
        }

        fun close() {
            closed = true
            thread?.interrupt()
        }

        private fun loop() {
            try {
                while (!closed) {
                    val line = queue.poll(1, TimeUnit.SECONDS) ?: continue
                    val lost = dropped.getAndSet(0)
                    if (lost > 0) {
                        conn.send("{\"event\":\"dropped\",\"time\":${System.currentTimeMillis()},\"display\":-1,\"count\":$lost}")
                    }
                    conn.send(line)
                }
            } catch (_: InterruptedException) {
            } catch (e: IOException) {
                Log.d(TAG, "event subscriber gone: $e")
            } finally {
                subscribers.remove(this)
            }
        }
    }

    companion object {
        private const val TAG = "PinkyEvents"
        private const val CONTENT_INTERVAL_MS = 100L
        private const val QUEUE_CAPACITY = 2048

        const val KIND_WINDOW = "window"
        const val KIND_WINDOWS = "windows"
        const val KIND_CONTENT = "content"
        const val KIND_TOAST = "toast"
        const val KIND_NOTIFICATION = "notification"

        val ALL_KINDS = setOf(KIND_WINDOW, KIND_WINDOWS, KIND_CONTENT, KIND_TOAST, KIND_NOTIFICATION)
        val DEFAULT_KINDS = setOf(KIND_WINDOW, KIND_TOAST, KIND_NOTIFICATION)

        /** Converts an event's uptime timestamp to wall-clock milliseconds. */
        fun eventTime(e: AccessibilityEvent): Long =
            System.currentTimeMillis() - (SystemClock.uptimeMillis() - e.eventTime)

        private fun joinText(e: AccessibilityEvent): String =
            e.text.filterNotNull().joinToString(" ") { it.toString() }.trim()

        private fun windowChangeNames(changes: Int): List<String> {
            val names = mutableListOf<String>()
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_ADDED != 0) names += "added"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_REMOVED != 0) names += "removed"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_TITLE != 0) names += "title"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_BOUNDS != 0) names += "bounds"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_LAYER != 0) names += "layer"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_ACTIVE != 0) names += "active"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_FOCUSED != 0) names += "focused"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_ACCESSIBILITY_FOCUSED != 0) names += "accessibility_focused"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_PARENT != 0) names += "parent"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_CHILDREN != 0) names += "children"
            if (changes and AccessibilityEvent.WINDOWS_CHANGE_PIP != 0) names += "pip"
            return names
        }
    }
}
