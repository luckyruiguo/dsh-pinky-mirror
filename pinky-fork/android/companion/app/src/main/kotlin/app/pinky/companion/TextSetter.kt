package app.pinky.companion

import android.app.KeyguardManager
import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.graphics.Rect
import android.os.Build
import android.os.Bundle
import android.os.PersistableBundle
import android.os.SystemClock
import android.text.InputType
import android.util.Log
import android.view.Display
import android.view.KeyEvent
import android.view.accessibility.AccessibilityNodeInfo
import android.view.accessibility.AccessibilityWindowInfo
import android.view.inputmethod.EditorInfo
import org.json.JSONObject

/**
 * Writes text into an editable node. It tries, in order:
 *
 * 1. ACTION_SET_TEXT: the text goes straight into the field, so any script
 *    works and the clipboard and the keyboard stay untouched.
 * 2. The app's own InputConnection, through the service's input method
 *    (Android 13+, FLAG_INPUT_METHOD_EDITOR): for views that refuse
 *    ACTION_SET_TEXT, such as a messaging app's own chat input. The text is
 *    committed as a whole, as a keyboard commits a chosen word, so a
 *    keyboard that composes (Pinyin, Japanese, Korean) is not involved.
 * 3. ACTION_PASTE, with the text on the clipboard only for the paste: marked
 *    sensitive, so Android does not show it, and cleared afterwards. The
 *    clipboard's earlier content is lost: Android lets only the app in front
 *    and the keyboard read it, so it cannot be put back.
 *
 * Each step after the first is taken only when the one before was refused
 * or left the field as it was, so the text never goes in twice. Only call
 * it on the accessibility worker thread.
 */
class TextSetter(private val service: PinkyAccessibilityService) {

    fun setText(req: JSONObject): String {
        if (!req.has("text")) throw RequestError("bad_request", "set_text needs text")
        val text = req.getString("text")
        val append = req.optBoolean("append")
        val submit = req.optBoolean("submit")
        val display = req.optInt("display", Display.DEFAULT_DISPLAY)
        service.displays.check(display)
        val node = resolve(req.optJSONObject("target"), display)
        try {
            if (!node.isEditable) throw RequestError("not_editable", "the target node is not editable")
            if (!node.isEnabled) throw RequestError("disabled", "the target node is disabled")
            if (append && node.isPassword) {
                throw RequestError("unsupported", "cannot append to a password field: its text is unreadable")
            }
            val w = Write(node, text, append, currentText(node))

            if (submit && !node.isFocused) node.performAction(AccessibilityNodeInfo.ACTION_FOCUS)
            val method = bySetText(w) ?: byInputConnection(w) ?: byPaste(w)
            if (method == null) {
                // The reasons name actions and counts, never the text.
                val why = w.refused.joinToString("; ")
                Log.i(TAG, "set_text refused: $why")
                throw RequestError("action_failed", why)
            }
            if (w.refused.isNotEmpty()) Log.i(TAG, "set_text by $method after: ${w.refused.joinToString("; ")}")

            node.refresh()
            val password = node.isPassword
            val after = if (password) "" else currentText(node)
            val verified = !password && after == w.value
            // A field that reads otherwise (a length limit, a filter) is not
            // submitted: the client reports it, and a message must not go
            // out other than it was asked.
            var submitted = false
            if (submit && (password || verified)) submitted = submit(node)

            val j = Json()
            j.beginObject()
            j.field("window", node.windowId)
            j.field("node", node.hashCode())
            j.field("method", method)
            if (password) {
                j.field("password", true)
            } else {
                j.field("text", after)
                j.field("verified", verified)
            }
            if (submit) j.field("submitted", submitted)
            j.endObject()
            return j.toString()
        } finally {
            TreeDumper.recycle(node)
        }
    }

    /** One write: the node, what to type, and what the field held before. */
    private class Write(
        val node: AccessibilityNodeInfo,
        val text: String,
        val append: Boolean,
        val before: String,
    ) {
        /** What the field should read; byInputConnection corrects it for a hint. */
        var value = if (append) before + text else text
        val refused = ArrayList<String>()
    }

    private fun bySetText(w: Write): String? {
        val args = Bundle()
        args.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, w.value)
        if (!w.node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)) {
            w.refused += if (offers(w.node, AccessibilityNodeInfo.ACTION_SET_TEXT)) {
                "the app did not accept ACTION_SET_TEXT"
            } else {
                "the field does not offer ACTION_SET_TEXT"
            }
            return null
        }
        if (!landed(w)) {
            w.refused += "the app accepted ACTION_SET_TEXT, but the field did not change"
            return null
        }
        return METHOD_SET_TEXT
    }

    /**
     * Commits the text through the InputConnection the app gave the
     * keyboard, which Android also hands this service. It is used only when
     * that connection is the target's: same app, and the text around its
     * cursor is the node's text.
     */
    private fun byInputConnection(w: Write): String? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            w.refused += "input connection: needs Android 13"
            return null
        }
        val im = service.inputMethod
        if (im == null) {
            w.refused += "input connection: the service is not an input method"
            return null
        }
        val node = w.node
        if (!node.isFocused) node.performAction(AccessibilityNodeInfo.ACTION_FOCUS)
        // The connection follows input focus, and comes a moment after it.
        val deadline = SystemClock.uptimeMillis() + CONNECT_MS
        var ic = im.currentInputConnection
        var info = im.currentInputEditorInfo
        while (!(im.currentInputStarted && ic != null && info?.packageName == node.packageName?.toString())) {
            if (SystemClock.uptimeMillis() >= deadline) {
                w.refused += if (ic == null || !im.currentInputStarted) {
                    "input connection: no field is connected to the keyboard"
                } else {
                    "input connection: the keyboard is connected to another app"
                }
                return null
            }
            SystemClock.sleep(POLL_MS)
            ic = im.currentInputConnection
            info = im.currentInputEditorInfo
        }
        val around = ic!!.getSurroundingText(AROUND, AROUND, 0)
        if (around == null) {
            w.refused += "input connection: the app did not answer"
            return null
        }
        val password = node.isPassword
        val known = around.text?.toString() ?: ""
        // A custom view may report its hint as its text while it is empty,
        // and its connection then reads empty. The connection is still the
        // node's when the node has input focus.
        node.refresh()
        val hint = !password && known.isEmpty() && w.before.isNotEmpty() && node.isFocused
        if (if (password) !passwordInput(info!!.inputType) else !(hint || holds(w.before, known, around.offset))) {
            w.refused += "input connection: the keyboard is connected to another field"
            return null
        }
        if (hint) w.value = w.text
        val length = when {
            password -> around.offset.coerceAtLeast(0) + known.length
            hint -> 0
            else -> w.before.length
        }
        if (w.append) {
            ic.setSelection(length, length)
        } else {
            ic.performContextMenuAction(android.R.id.selectAll)
            ic.setSelection(0, length)
        }
        ic.commitText(w.text, 1, null)
        if (!landed(w)) {
            w.refused += "input connection: the field did not change"
            return null
        }
        return METHOD_INPUT_CONNECTION
    }

    /**
     * Pastes the text with ACTION_PASTE. The field must have focus, since
     * TextView pastes only then; its whole text is selected first unless
     * appending, so that the paste replaces it. A TextView offers
     * ACTION_PASTE only while the clipboard holds something, so the action
     * is tried whether it is offered or not.
     */
    private fun byPaste(w: Write): String? {
        val node = w.node
        val clipboard = service.getSystemService(ClipboardManager::class.java)
        if (clipboard == null) {
            w.refused += "paste: no clipboard"
            return null
        }
        if (!node.isFocused) node.performAction(AccessibilityNodeInfo.ACTION_FOCUS)
        val sel = Bundle()
        sel.putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_START_INT, if (w.append) w.before.length else 0)
        sel.putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_END_INT, w.before.length)
        node.performAction(AccessibilityNodeInfo.ACTION_SET_SELECTION, sel)
        val clip = ClipData.newPlainText("Pinky", w.text)
        // Keeps the text off Android's clipboard preview (Android 13+); one
        // -time codes go this way too.
        clip.description.extras = PersistableBundle().apply { putBoolean(EXTRA_IS_SENSITIVE, true) }
        try {
            clipboard.setPrimaryClip(clip)
            if (!node.performAction(AccessibilityNodeInfo.ACTION_PASTE)) {
                // Android lets an app read the clipboard only while it has
                // focus; on another display it seems not to while the
                // phone is locked.
                val locked = service.getSystemService(KeyguardManager::class.java)?.isKeyguardLocked == true
                w.refused += "the app did not accept ACTION_PASTE" + if (locked) " (apps cannot paste while the phone is locked)" else ""
                return null
            }
            if (!landed(w)) {
                w.refused += "paste: the field did not change"
                return null
            }
            return METHOD_PASTE
        } catch (e: RuntimeException) {
            // SecurityException and the like: the clipboard refused the clip.
            w.refused += "paste: ${e.javaClass.simpleName}"
            return null
        } finally {
            try {
                clipboard.clearPrimaryClip()
            } catch (e: RuntimeException) {
                Log.w(TAG, "cannot clear the clipboard", e)
            }
        }
    }

    /**
     * Waits until the field reads otherwise than before, or as the write
     * wants, and says whether the write went in: false when the field still
     * reads as before after LAND_MS. An app may take the text a moment
     * after the action (a web view, an input connection's own thread). A
     * password field cannot be read, so it is given a moment and counted
     * as written.
     */
    private fun landed(w: Write): Boolean {
        if (w.node.isPassword) {
            if (w.value != w.before) SystemClock.sleep(POLL_MS * 4)
            return true
        }
        val deadline = SystemClock.uptimeMillis() + LAND_MS
        while (true) {
            w.node.refresh()
            val now = currentText(w.node)
            if (now == w.value || now != w.before) return true
            if (SystemClock.uptimeMillis() >= deadline) return false
            SystemClock.sleep(POLL_MS)
        }
    }

    /**
     * Runs the field's IME action: ACTION_IME_ENTER (Android 11+, what a
     * TextView handles), else the editor action through the input
     * connection, else Enter through it.
     */
    private fun submit(node: AccessibilityNodeInfo): Boolean {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R &&
            node.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_IME_ENTER.id)
        ) {
            return true
        }
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return false
        val im = service.inputMethod ?: return false
        val ic = im.currentInputConnection ?: return false
        val info = im.currentInputEditorInfo ?: return false
        if (!im.currentInputStarted || info.packageName != node.packageName?.toString()) return false
        val action = info.imeOptions and EditorInfo.IME_MASK_ACTION
        if (info.imeOptions and EditorInfo.IME_FLAG_NO_ENTER_ACTION == 0 &&
            action != EditorInfo.IME_ACTION_NONE && action != EditorInfo.IME_ACTION_UNSPECIFIED
        ) {
            ic.performEditorAction(action)
        } else {
            ic.sendKeyEvent(KeyEvent(KeyEvent.ACTION_DOWN, KeyEvent.KEYCODE_ENTER))
            ic.sendKeyEvent(KeyEvent(KeyEvent.ACTION_UP, KeyEvent.KEYCODE_ENTER))
        }
        return true
    }

    private fun currentText(n: AccessibilityNodeInfo): String =
        if (n.isShowingHintText) "" else n.text?.toString() ?: ""

    /**
     * Finds the node a request targets on one display: the focused input, a
     * node handle from a tree response, the editable node under a point, or
     * the first editable node with a resource id. Windows are searched
     * top-most first.
     */
    private fun resolve(t: JSONObject?, display: Int): AccessibilityNodeInfo {
        val windows = service.displays.windowsOn(display).sortedByDescending { it.layer }
        try {
            if (t == null || t.length() == 0 || t.optBoolean("focused")) {
                return focusedOn(windows)
            }
            if (t.has("node")) {
                val handle = t.getInt("node")
                val windowId = t.optInt("window", -1)
                for (w in windows) {
                    if (windowId != -1 && w.id != windowId) continue
                    find(w) { it.hashCode() == handle }?.let { return it }
                }
                throw RequestError("not_found", "node $handle is gone")
            }
            val id = t.optString("id", "")
            if (t.has("x") && t.has("y")) {
                val x = t.getInt("x")
                val y = t.getInt("y")
                for (w in windows) {
                    smallestEditableAt(w, x, y, id)?.let { return it }
                }
                throw RequestError("not_found", "no editable node at $x,$y")
            }
            if (id.isNotEmpty()) {
                for (w in windows) {
                    find(w) { it.isEditable && it.viewIdResourceName == id }?.let { return it }
                }
                throw RequestError("not_found", "no editable node with id $id")
            }
            throw RequestError("bad_request", "target needs focused, node, x and y, or id")
        } finally {
            TreeDumper.recycleWindows(windows)
        }
    }

    /**
     * The input focus among a display's windows. The phone's input focus is
     * on the display that was touched last, and is used when it is in one of
     * these windows. A display that was not touched last has no focused
     * window, but its views keep their focus: then the window that would get
     * the focus is searched, the top-most application window. A dialog
     * without a focused field so gives `not_found` rather than the field
     * behind it.
     */
    private fun focusedOn(windows: List<AccessibilityWindowInfo>): AccessibilityNodeInfo {
        service.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)?.let { n ->
            if (windows.any { it.id == n.windowId }) return n
            TreeDumper.recycle(n)
        }
        val w = windows.firstOrNull { it.isFocused && it.type != AccessibilityWindowInfo.TYPE_INPUT_METHOD }
            ?: windows.filter { it.type == AccessibilityWindowInfo.TYPE_APPLICATION }.maxByOrNull { it.layer }
        val root = w?.root
        if (root != null) {
            val focus = root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            TreeDumper.recycle(root)
            if (focus != null) return focus
        }
        throw RequestError("not_found", "no input has focus")
    }

    private fun find(w: AccessibilityWindowInfo, match: (AccessibilityNodeInfo) -> Boolean): AccessibilityNodeInfo? {
        val root = w.root ?: return null
        val stack = ArrayDeque<AccessibilityNodeInfo>()
        stack.addLast(root)
        var found: AccessibilityNodeInfo? = null
        while (stack.isNotEmpty()) {
            val n = stack.removeLast()
            if (found == null && n.isVisibleToUser && match(n)) {
                found = n
                continue
            }
            if (found == null) {
                for (i in n.childCount - 1 downTo 0) n.getChild(i)?.let { stack.addLast(it) }
            }
            TreeDumper.recycle(n)
        }
        return found
    }

    private fun smallestEditableAt(w: AccessibilityWindowInfo, x: Int, y: Int, id: String): AccessibilityNodeInfo? {
        val root = w.root ?: return null
        val r = Rect()
        val stack = ArrayDeque<AccessibilityNodeInfo>()
        stack.addLast(root)
        var best: AccessibilityNodeInfo? = null
        var bestScore = Long.MAX_VALUE
        while (stack.isNotEmpty()) {
            val n = stack.removeLast()
            var keep = false
            if (n.isVisibleToUser) {
                n.getBoundsInScreen(r)
                if (r.contains(x, y)) {
                    if (n.isEditable) {
                        // Prefer a matching resource id, then the smallest area.
                        val area = r.width().toLong() * r.height().toLong()
                        val score = if (id.isNotEmpty() && n.viewIdResourceName != id) area + (1L shl 40) else area
                        if (score < bestScore) {
                            best?.let { TreeDumper.recycle(it) }
                            best = n
                            bestScore = score
                            keep = true
                        }
                    }
                    for (i in 0 until n.childCount) n.getChild(i)?.let { stack.addLast(it) }
                }
            }
            if (!keep) TreeDumper.recycle(n)
        }
        return best
    }

    companion object {
        private const val TAG = "PinkyText"

        const val METHOD_SET_TEXT = "set_text"
        const val METHOD_INPUT_CONNECTION = "input_connection"
        const val METHOD_PASTE = "paste"

        /** ClipDescription.EXTRA_IS_SENSITIVE, which some phones honour before Android 13 too. */
        private const val EXTRA_IS_SENSITIVE = "android.content.extra.IS_SENSITIVE"

        private const val CONNECT_MS = 1000L
        private const val LAND_MS = 1500L
        private const val POLL_MS = 50L
        private const val AROUND = 4096

        private fun offers(n: AccessibilityNodeInfo, action: Int): Boolean =
            n.actionList.any { it.id == action }

        /**
         * Whether a field's text holds what an input connection reports
         * around its cursor, at that offset when it is known.
         */
        internal fun holds(field: String, around: String, offset: Int): Boolean {
            if (around.isEmpty()) return field.isEmpty()
            if (offset < 0) return field.contains(around)
            return offset + around.length <= field.length && field.regionMatches(offset, around, 0, around.length)
        }

        private fun passwordInput(type: Int): Boolean {
            val variation = type and InputType.TYPE_MASK_VARIATION
            return when (type and InputType.TYPE_MASK_CLASS) {
                InputType.TYPE_CLASS_TEXT -> variation == InputType.TYPE_TEXT_VARIATION_PASSWORD ||
                    variation == InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD ||
                    variation == InputType.TYPE_TEXT_VARIATION_WEB_PASSWORD
                InputType.TYPE_CLASS_NUMBER -> variation == InputType.TYPE_NUMBER_VARIATION_PASSWORD
                else -> false
            }
        }
    }
}
