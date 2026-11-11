package app.pinky.inputtest

import android.app.Activity
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.widget.EditText
import android.widget.TextView

/**
 * Fields that take text in different ways, and a line that echoes
 * what each holds, so a test can read the result from the screen even
 * when a field does not report its text:
 *
 * - plain: an ordinary EditText.
 * - no_set_text: an EditText that refuses ACTION_SET_TEXT, as some apps'
 *   own subclasses do.
 * - no_commit: an EditText that refuses ACTION_SET_TEXT and drops what its
 *   InputConnection commits; only a paste fills it.
 * - chat: a view that is not a TextView, with an InputConnection of its
 *   own, built like a messaging app's chat input. It refuses
 *   ACTION_SET_TEXT and ACTION_PASTE and reports its hint as its text while
 *   empty; text reaches it only through its InputConnection or as keys.
 * - short: an EditText that keeps at most 4 characters, so longer text
 *   reads otherwise afterwards.
 * - dead: a chat input that takes no text in any way.
 */
class MainActivity : Activity() {

    private lateinit var plain: EditText
    private lateinit var noSetText: EditText
    private lateinit var noCommit: EditText
    private lateinit var chat: ChatInput
    private lateinit var short: EditText
    private lateinit var echo: TextView
    private var sent = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        plain = findViewById(R.id.plain)
        noSetText = findViewById(R.id.no_set_text)
        noCommit = findViewById(R.id.no_commit)
        chat = findViewById(R.id.chat)
        short = findViewById(R.id.short_field)
        echo = findViewById(R.id.echo)
        val watcher = object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
            override fun afterTextChanged(s: Editable?) = update()
        }
        plain.addTextChangedListener(watcher)
        noSetText.addTextChangedListener(watcher)
        noCommit.addTextChangedListener(watcher)
        short.addTextChangedListener(watcher)
        chat.onChange = { update() }
        chat.onSend = { text ->
            sent = text
            update()
        }
        update()
    }

    private fun update() {
        echo.text = "plain=[${plain.text}] no_set_text=[${noSetText.text}] no_commit=[${noCommit.text}] chat=[${chat.text}] short=[${short.text}] sent=[$sent]"
    }
}
