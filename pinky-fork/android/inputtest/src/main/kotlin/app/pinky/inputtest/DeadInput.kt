package app.pinky.inputtest

import android.content.Context
import android.util.AttributeSet

/**
 * A chat input that takes no text in any way: committed text and typed
 * letters are dropped. Typing into it must end in an error that says the
 * text did not reach the field.
 */
class DeadInput(context: Context, attrs: AttributeSet?) : ChatInput(context, attrs) {
    override val takesText = false
}
