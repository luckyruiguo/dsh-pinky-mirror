package dev.pinky.crashtest;

import android.app.Activity;
import android.os.Bundle;
import android.util.Log;
import android.widget.Button;
import android.widget.LinearLayout;

public class Main extends Activity {
    private static final String TAG = "CrashTest";

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        Log.i(TAG, "Main created");
        LinearLayout box = new LinearLayout(this);
        box.setOrientation(LinearLayout.VERTICAL);
        // Below the status and action bars, which apps targeting Android 15 draw under.
        box.setPadding(48, 480, 48, 48);
        Button log = new Button(this);
        log.setText("Log");
        log.setOnClickListener(v -> Log.w(TAG, "Log button tapped"));
        Button crash = new Button(this);
        crash.setText("Crash");
        crash.setOnClickListener(v -> {
            Log.e(TAG, "About to crash");
            throw new IllegalStateException("Crash button tapped");
        });
        Button hang = new Button(this);
        hang.setText("Hang");
        hang.setOnClickListener(v -> {
            Log.w(TAG, "Hanging the main thread");
            try { Thread.sleep(20000); } catch (InterruptedException e) { }
        });
        box.addView(log);
        box.addView(crash);
        box.addView(hang);
        setContentView(box);
    }
}
