package app.pinky.testapp;

import android.app.Activity;
import android.content.pm.PackageInfo;
import android.os.Bundle;
import android.widget.TextView;

// The test app phone_app's tests install: one screen that names its version.
public class MainActivity extends Activity {
    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        String version = "?";
        try {
            PackageInfo p = getPackageManager().getPackageInfo(getPackageName(), 0);
            version = p.versionName;
        } catch (Exception e) {
            // keep "?"
        }
        TextView t = new TextView(this);
        t.setText(getString(R.string.hello) + " " + version);
        t.setTextSize(24);
        t.setPadding(48, 160, 48, 48);
        setContentView(t);
    }
}
