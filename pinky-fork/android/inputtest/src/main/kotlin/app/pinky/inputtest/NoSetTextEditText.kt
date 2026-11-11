package app.pinky.inputtest

import android.content.Context
import android.os.Bundle
import android.util.AttributeSet
import android.view.accessibility.AccessibilityNodeInfo
import android.widget.EditText

/** An EditText that refuses ACTION_SET_TEXT and does not offer it. */
class NoSetTextEditText(context: Context, attrs: AttributeSet?) : EditText(context, attrs) {

    override fun onInitializeAccessibilityNodeInfo(info: AccessibilityNodeInfo) {
        super.onInitializeAccessibilityNodeInfo(info)
        info.removeAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SET_TEXT)
    }

    override fun performAccessibilityAction(action: Int, arguments: Bundle?): Boolean =
        if (action == AccessibilityNodeInfo.ACTION_SET_TEXT) false else super.performAccessibilityAction(action, arguments)
}
