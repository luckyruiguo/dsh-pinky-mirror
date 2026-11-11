package app.pinky.inputtest

import android.content.Context
import android.os.Bundle
import android.util.AttributeSet
import android.view.accessibility.AccessibilityNodeInfo
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputConnectionWrapper
import android.view.inputmethod.TextAttribute
import android.widget.EditText

/**
 * An EditText that refuses ACTION_SET_TEXT and whose InputConnection drops
 * committed text, so only ACTION_PASTE (or keys) can fill it: the companion
 * must find that its input connection left the field as it was and paste.
 */
class NoCommitEditText(context: Context, attrs: AttributeSet?) : EditText(context, attrs) {

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection? {
        val ic = super.onCreateInputConnection(outAttrs) ?: return null
        return object : InputConnectionWrapper(ic, false) {
            override fun commitText(text: CharSequence?, newCursorPosition: Int) = true

            override fun commitText(text: CharSequence, newCursorPosition: Int, attribute: TextAttribute?) = true
        }
    }

    override fun onInitializeAccessibilityNodeInfo(info: AccessibilityNodeInfo) {
        super.onInitializeAccessibilityNodeInfo(info)
        info.removeAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SET_TEXT)
    }

    override fun performAccessibilityAction(action: Int, arguments: Bundle?): Boolean =
        if (action == AccessibilityNodeInfo.ACTION_SET_TEXT) false else super.performAccessibilityAction(action, arguments)
}
