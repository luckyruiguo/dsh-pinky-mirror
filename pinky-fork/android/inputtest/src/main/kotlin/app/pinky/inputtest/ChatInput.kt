package app.pinky.inputtest

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.text.Editable
import android.text.InputType
import android.text.Selection
import android.text.TextPaint
import android.util.AttributeSet
import android.util.TypedValue
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputMethodManager
import android.widget.EditText

/**
 * A text field that is not a TextView, like a messaging app's own chat
 * input: it draws its text itself and takes input through an
 * InputConnection of its own. Accessibility sees an editable node with the
 * text, but the view refuses ACTION_SET_TEXT and ACTION_PASTE, and it
 * ignores the paste key. While empty it reports its hint as its text, as
 * some apps' own inputs do. Typed keys (letters, Delete, Enter) work as in
 * a TextView; the keyboard's action sends the text.
 */
open class ChatInput(context: Context, attrs: AttributeSet?) : View(context, attrs) {

    /** Whether text reaches the field at all; DeadInput takes none. */
    protected open val takesText = true

    val text: Editable = Editable.Factory.getInstance().newEditable("")
    var onChange: (() -> Unit)? = null
    var onSend: ((String) -> Unit)? = null

    private val imm = context.getSystemService(InputMethodManager::class.java)
    private val paint = TextPaint(Paint.ANTI_ALIAS_FLAG).apply {
        textSize = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_SP, 18f, resources.displayMetrics)
        color = Color.BLACK
    }
    private val hintPaint = TextPaint(paint).apply { color = Color.GRAY }

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        isClickable = true
        Selection.setSelection(text, 0)
    }

    override fun onCheckIsTextEditor() = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
        outAttrs.imeOptions = EditorInfo.IME_ACTION_SEND or EditorInfo.IME_FLAG_NO_EXTRACT_UI or
            EditorInfo.IME_FLAG_NO_FULLSCREEN
        outAttrs.initialSelStart = Selection.getSelectionStart(text)
        outAttrs.initialSelEnd = Selection.getSelectionEnd(text)
        return Connection()
    }

    private inner class Connection : BaseInputConnection(this@ChatInput, true) {
        override fun getEditable(): Editable = text

        override fun commitText(t: CharSequence?, newCursorPosition: Int) =
            !takesText || super.commitText(t, newCursorPosition).also { changed() }

        override fun setComposingText(t: CharSequence?, newCursorPosition: Int) =
            !takesText || super.setComposingText(t, newCursorPosition).also { changed() }

        override fun finishComposingText() = super.finishComposingText().also { changed() }

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int) =
            super.deleteSurroundingText(beforeLength, afterLength).also { changed() }

        override fun setSelection(start: Int, end: Int) = super.setSelection(start, end).also { changed() }

        override fun performContextMenuAction(id: Int): Boolean {
            when (id) {
                android.R.id.selectAll -> Selection.setSelection(text, 0, text.length)
                else -> return false
            }
            changed()
            return true
        }

        override fun performEditorAction(actionCode: Int): Boolean {
            send()
            return true
        }
    }

    override fun onTouchEvent(event: MotionEvent): Boolean {
        if (event.action == MotionEvent.ACTION_UP) {
            requestFocus()
            imm.showSoftInput(this, 0)
            performClick()
        }
        return true
    }

    override fun performClick(): Boolean {
        super.performClick()
        return true
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        when {
            keyCode == KeyEvent.KEYCODE_DEL -> {
                val start = Selection.getSelectionStart(text)
                val end = Selection.getSelectionEnd(text)
                if (start != end) text.delete(minOf(start, end), maxOf(start, end))
                else if (start > 0) text.delete(start - 1, start)
            }
            keyCode == KeyEvent.KEYCODE_ENTER -> send()
            event.isCtrlPressed && keyCode == KeyEvent.KEYCODE_A -> Selection.setSelection(text, 0, text.length)
            event.unicodeChar != 0 && !event.isCtrlPressed -> if (takesText) replaceSelection(event.unicodeChar.toChar().toString())
            else -> return super.onKeyDown(keyCode, event)
        }
        changed()
        return true
    }

    private fun replaceSelection(s: String) {
        val start = Selection.getSelectionStart(text).coerceAtLeast(0)
        val end = Selection.getSelectionEnd(text).coerceAtLeast(0)
        text.replace(minOf(start, end), maxOf(start, end), s)
    }

    private fun send() {
        onSend?.invoke(text.toString())
        text.clear()
        changed()
    }

    private fun changed() {
        invalidate()
        imm.updateSelection(
            this, Selection.getSelectionStart(text), Selection.getSelectionEnd(text),
            BaseInputConnection.getComposingSpanStart(text), BaseInputConnection.getComposingSpanEnd(text),
        )
        sendAccessibilityEvent(AccessibilityEvent.TYPE_VIEW_TEXT_CHANGED)
        onChange?.invoke()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val y = height / 2f - (paint.descent() + paint.ascent()) / 2
        if (text.isEmpty()) canvas.drawText(HINT, paddingLeft + 24f, y, hintPaint)
        else canvas.drawText(text, 0, text.length, paddingLeft + 24f, y, paint)
    }

    override fun getAccessibilityClassName(): CharSequence = EditText::class.java.name

    override fun onInitializeAccessibilityNodeInfo(info: AccessibilityNodeInfo) {
        super.onInitializeAccessibilityNodeInfo(info)
        info.isEditable = true
        info.text = if (text.isEmpty()) HINT else text.toString()
        info.setTextSelection(Selection.getSelectionStart(text), Selection.getSelectionEnd(text))
    }

    override fun performAccessibilityAction(action: Int, arguments: android.os.Bundle?): Boolean = when (action) {
        AccessibilityNodeInfo.ACTION_SET_TEXT, AccessibilityNodeInfo.ACTION_PASTE -> false
        else -> super.performAccessibilityAction(action, arguments)
    }

    private companion object {
        const val HINT = "Message"
    }
}
