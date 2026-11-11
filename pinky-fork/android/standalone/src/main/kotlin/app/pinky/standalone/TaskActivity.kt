package app.pinky.standalone

import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.text.InputType
import android.util.TypedValue
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.ScrollView
import android.widget.TextView
import app.pinky.companion.R
import org.json.JSONObject
import java.io.Closeable

/**
 * Pinky's own screen, in a process of its own (":ui"): the person types a
 * task and the model's key, chooses when agents ask, starts and stops the
 * task and follows its steps. Approvals are answered on the card over
 * other apps (Overlays).
 *
 * The agent cannot use this screen: the gateway refuses every action
 * while it is in front and hides what it shows (policy.StandaloneApp),
 * OwnUI refuses gestures on it, and its content is
 * accessibilityDataSensitive. Only its root stays visible to the service,
 * which needs it to see that Pinky is in front.
 */
class TaskActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private val prefs by lazy { getSharedPreferences("task", MODE_PRIVATE) }
    private var sub: Closeable? = null
    private var shownPreset = -1
    private var settingPreset = false

    private lateinit var status: TextView
    private lateinit var setup: Button
    private lateinit var task: EditText
    private lateinit var provider: RadioGroup
    private lateinit var model: EditText
    private lateinit var baseUrl: EditText
    private lateinit var key: EditText
    private lateinit var preset: RadioGroup
    private lateinit var start: Button
    private lateinit var stop: Button
    private lateinit var newTask: Button
    private lateinit var error: TextView
    private lateinit var agentLine: TextView
    private lateinit var steps: LinearLayout

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(px(20), px(24), px(20), px(24))
        }
        content.addView(text(getString(R.string.standalone_app_name), 24f, bold = true))
        status = text("", 14f).also { content.addView(it) }
        setup = Button(this).apply {
            text = getString(R.string.standalone_turn_on)
            visibility = View.GONE
            setOnClickListener { startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)) }
        }
        content.addView(setup)

        content.addView(label(R.string.standalone_task))
        task = EditText(this).apply {
            hint = getString(R.string.standalone_task_hint)
            minLines = 3
            gravity = android.view.Gravity.TOP
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or
                InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
        }
        content.addView(task)

        content.addView(label(R.string.standalone_model))
        provider = RadioGroup(this).apply { orientation = RadioGroup.HORIZONTAL }
        provider.addView(radio(R.string.standalone_anthropic, ID_ANTHROPIC))
        provider.addView(radio(R.string.standalone_openai, ID_OPENAI))
        provider.check(if (prefs.getString("provider", "") == "openai") ID_OPENAI else ID_ANTHROPIC)
        content.addView(provider)
        model = field(R.string.standalone_model_hint, prefs.getString("model", "").orEmpty())
        content.addView(model)
        baseUrl = field(R.string.standalone_base_url_hint, prefs.getString("base_url", "").orEmpty())
        baseUrl.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
        content.addView(baseUrl)
        // The key stays in this screen's memory and the core's until the
        // task ends: never saved, never in the saved state, never autofilled.
        key = field(R.string.standalone_key_hint, "").apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
            typeface = Typeface.DEFAULT
            isSaveEnabled = false
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
        }
        content.addView(key)

        content.addView(label(R.string.standalone_ask_before))
        preset = RadioGroup(this)
        preset.addView(radio(R.string.standalone_preset_payments, 2))
        preset.addView(radio(R.string.standalone_preset_commits, 1))
        preset.addView(radio(R.string.standalone_preset_every, 3))
        preset.setOnCheckedChangeListener { _, id ->
            if (settingPreset || id == shownPreset) return@setOnCheckedChangeListener
            CoreClient.send(CoreClient.cmd("preset", "preset" to id)) { err -> main.post { showError(err) } }
        }
        content.addView(preset)

        val row = LinearLayout(this).apply { setPadding(0, px(12), 0, 0) }
        start = Button(this).apply {
            text = getString(R.string.standalone_start)
            setOnClickListener { startTask(this) }
        }
        stop = Button(this).apply {
            text = getString(R.string.standalone_stop)
            isEnabled = false
            setOnClickListener { CoreClient.send(CoreClient.cmd("stop")) { err -> main.post { showError(err) } } }
        }
        newTask = Button(this).apply {
            text = getString(R.string.standalone_new_task)
            isEnabled = false
            setOnClickListener {
                task.setText("")
                CoreClient.send(CoreClient.cmd("dismiss")) { err -> main.post { showError(err) } }
            }
        }
        row.addView(start)
        row.addView(stop)
        row.addView(newTask)
        content.addView(row)
        error = text("", 14f).apply {
            setTextColor(Color.rgb(220, 60, 50))
            visibility = View.GONE
        }
        content.addView(error)
        agentLine = text("", 15f, bold = true).apply { setPadding(0, px(16), 0, px(4)) }
        content.addView(agentLine)
        steps = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        content.addView(steps)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            content.setAccessibilityDataSensitive(View.ACCESSIBILITY_DATA_SENSITIVE_YES)
        }
        content.filterTouchesWhenObscured = true
        actionBar?.hide()
        val scroll = ScrollView(this).apply { addView(content) }
        // Edge to edge (Android 15 and later): keep the content clear of
        // the bars and the keyboard. Before Android 11 the window is not
        // edge to edge and adjustResize does it.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            scroll.setOnApplyWindowInsetsListener { v, insets ->
                val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.ime())
                v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
                insets
            }
        }
        setContentView(scroll)
    }

    override fun onStart() {
        super.onStart()
        sub = CoreClient.subscribe({ st -> main.post { render(st) } }, { why -> main.post { down(why) } })
    }

    override fun onStop() {
        sub?.close()
        sub = null
        super.onStop()
    }

    private fun startTask(button: Button) {
        val t = task.text.toString().trim()
        val k = key.text.toString().trim()
        if (t.isEmpty()) return showError(getString(R.string.standalone_need_task))
        if (k.isEmpty()) return showError(getString(R.string.standalone_need_key))
        val p = if (provider.checkedRadioButtonId == ID_OPENAI) "openai" else "anthropic"
        val m = model.text.toString().trim()
        val u = baseUrl.text.toString().trim()
        prefs.edit().putString("provider", p).putString("model", m).putString("base_url", u).apply()
        button.isEnabled = false
        showError(null)
        val req = CoreClient.cmd("start", "task" to t, "key" to k, "provider" to p, "model" to m, "base_url" to u)
        CoreClient.send(req) { err ->
            main.post {
                if (err != null) {
                    button.isEnabled = true
                    showError(err)
                } else {
                    // The agent starts from the app the person was in;
                    // the Working pill follows the task there.
                    moveTaskToBack(true)
                }
            }
        }
    }

    private fun render(st: JSONObject) {
        val device = st.optJSONObject("device")
        val ready = device?.optBoolean("ready") == true
        status.text = if (ready) {
            getString(R.string.standalone_ready, device?.optString("name").orEmpty())
        } else {
            getString(R.string.standalone_not_ready)
        }
        setup.visibility = if (ready) View.GONE else View.VISIBLE

        val p = st.optInt("preset", -1)
        if (p != shownPreset) {
            shownPreset = p
            settingPreset = true
            preset.check(p)
            settingPreset = false
        }

        val agent = st.optJSONObject("agent")
        val running = agent?.optBoolean("running") == true
        start.isEnabled = ready && !running
        stop.isEnabled = running
        newTask.isEnabled = agent != null && !running
        for (v in listOf(task, provider, model, baseUrl, key)) v.isEnabled = !running
        for (i in 0 until provider.childCount) provider.getChildAt(i).isEnabled = !running

        val needs = st.optJSONArray("needs")
        agentLine.text = when {
            agent == null -> ""
            running && needs != null && needs.length() > 0 ->
                getString(R.string.standalone_line_waiting, needs.getJSONObject(0).optString("title"))
            running -> getString(
                R.string.standalone_line_running,
                agent.optInt("steps"),
                agent.optLong("seconds"),
                agent.optDouble("cost", 0.0),
            )
            else -> getString(
                R.string.standalone_line_ended,
                agent.optString("end"),
                agent.optString("summary"),
                agent.optInt("steps"),
                agent.optLong("seconds"),
                agent.optDouble("cost", 0.0),
            )
        }
        if (agent != null && task.text.isEmpty()) task.setText(agent.optString("task"))

        steps.removeAllViews()
        val list = st.optJSONObject("task")?.optJSONArray("steps")
        if (list != null) {
            for (i in 0 until list.length()) {
                val s = list.getJSONObject(i)
                val mark = when (s.optString("status")) {
                    "settled", "" -> "✓"
                    "approval_pending", "paused" -> "…"
                    "approval_denied", "blocked_by_policy", "stopped", "error" -> "✕"
                    else -> "•"
                }
                val ms = s.optLong("ms")
                val line = if (ms > 0) "$mark  ${s.optString("text")} · $ms ms" else "$mark  ${s.optString("text")}"
                steps.addView(text(line, 14f).apply { setPadding(0, px(3), 0, px(3)) })
            }
        }
    }

    private fun down(why: String) {
        status.text = getString(R.string.standalone_core_down, why)
        setup.visibility = View.VISIBLE
        start.isEnabled = false
        stop.isEnabled = false
    }

    private fun showError(err: String?) {
        error.text = err.orEmpty()
        error.visibility = if (err.isNullOrEmpty()) View.GONE else View.VISIBLE
    }

    private fun text(s: String, sp: Float, bold: Boolean = false) = TextView(this).apply {
        text = s
        setTextSize(TypedValue.COMPLEX_UNIT_SP, sp)
        if (bold) setTypeface(typeface, Typeface.BOLD)
    }

    private fun label(id: Int) = text(getString(id), 13f, bold = true).apply {
        setPadding(0, px(18), 0, px(4))
        alpha = 0.7f
    }

    private fun field(hint: Int, value: String) = EditText(this).apply {
        this.hint = getString(hint)
        setText(value)
        isSingleLine = true
        layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
    }

    private fun radio(text: Int, id: Int) = RadioButton(this).apply {
        this.text = getString(text)
        this.id = id
    }

    private fun px(d: Int) = (d * resources.displayMetrics.density).toInt()

    companion object {
        private const val ID_ANTHROPIC = 101
        private const val ID_OPENAI = 102
    }
}
