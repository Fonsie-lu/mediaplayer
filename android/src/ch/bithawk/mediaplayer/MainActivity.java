package ch.bithawk.mediaplayer;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.content.DialogInterface;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.ActivityInfo;
import android.net.Uri;
import android.os.Bundle;
import android.text.InputType;
import android.view.KeyEvent;
import android.view.Menu;
import android.view.MenuItem;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowManager;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.EditText;
import android.widget.FrameLayout;

/**
 * A WebView wrapper around the mediaplayer web client, so the phone gets a
 * launcher icon and a full-screen window instead of a browser tab.
 *
 * The server URL is not compiled in: it lives in SharedPreferences, is asked
 * for on first launch, and is editable from the menu — the same APK works
 * after the server moves to another host or port.
 */
public class MainActivity extends Activity {

    private static final String PREFS = "mediaplayer";
    private static final String KEY_URL = "server_url";
    /** Prefill only — the LAN address of the machine the APK was built on. */
    private static final String DEFAULT_URL = "http://192.168.0.16:8090";

    /**
     * Attached on every page load. The web client never asks Android to keep
     * the screen on, and a phone dimming 30s into a film is the one thing that
     * makes a WebView app feel broken. Capture-phase listeners on document
     * catch the events of a <video> that does not bubble them.
     */
    private static final String KEEP_AWAKE_JS =
            "(function(){if(window.__mpAwake)return;window.__mpAwake=1;"
            + "document.addEventListener('play',function(){MPHost.awake(true)},true);"
            + "document.addEventListener('pause',function(){MPHost.awake(false)},true);"
            + "document.addEventListener('ended',function(){MPHost.awake(false)},true);})()";

    private WebView web;
    private FrameLayout root;

    // Set while the page is showing a fullscreen video (the player's `f` key
    // and the native control both route through onShowCustomView).
    private View customView;
    private WebChromeClient.CustomViewCallback customViewCallback;
    private int savedSystemUi;

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);

        root = new FrameLayout(this);
        web = new WebView(this);
        root.addView(web, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(root);

        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        // localStorage is not a nicety here: per-directory cursor memory, the
        // sort preference and resume positions all live in it.
        s.setDomStorageEnabled(true);
        // Without this the player's autoplay-after-seek is blocked and HLS
        // playback stalls waiting for a tap that already happened.
        s.setMediaPlaybackRequiresUserGesture(false);
        s.setUseWideViewPort(true);
        s.setLoadWithOverviewMode(true);
        s.setSupportZoom(false);
        s.setBuiltInZoomControls(false);
        // The session cookie (mp_sid) is what ties a browser to its transcode
        // session, so it has to survive across app launches.
        CookieManager.getInstance().setAcceptCookie(true);

        web.addJavascriptInterface(new Bridge(), "MPHost");
        web.setWebViewClient(new Client());
        web.setWebChromeClient(new Chrome());
        web.setBackgroundColor(0xff1a1b26);

        if (saved != null) {
            web.restoreState(saved);
            return;
        }
        String url = serverUrl();
        if (url == null) {
            askServer(true);
        } else {
            web.loadUrl(url);
        }
    }

    // ---------- server URL ----------

    private SharedPreferences prefs() {
        return getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    private String serverUrl() {
        return prefs().getString(KEY_URL, null);
    }

    /** first == true on the initial launch, where Cancel has nothing to fall back to. */
    private void askServer(final boolean first) {
        final EditText input = new EditText(this);
        input.setInputType(InputType.TYPE_TEXT_VARIATION_URI);
        input.setSingleLine(true);
        String current = serverUrl();
        input.setText(current != null ? current : DEFAULT_URL);
        input.setSelectAllOnFocus(true);

        AlertDialog.Builder b = new AlertDialog.Builder(this)
                .setTitle("Server address")
                .setMessage("Where the mediaplayer server is listening, e.g. http://192.168.0.16:8090")
                .setView(input)
                .setPositiveButton("Connect", new DialogInterface.OnClickListener() {
                    @Override
                    public void onClick(DialogInterface d, int which) {
                        String url = normalize(input.getText().toString());
                        if (url.isEmpty()) {
                            askServer(first);
                            return;
                        }
                        prefs().edit().putString(KEY_URL, url).apply();
                        // A different server means a different session and a
                        // different set of mounts; the old history would just
                        // walk back into dead pages.
                        web.clearHistory();
                        web.loadUrl(url);
                    }
                });
        if (!first) {
            b.setNegativeButton("Cancel", null);
        }
        b.setCancelable(!first);
        b.show();
    }

    /** Accepts "host:8090" as well as a full URL — a phone keyboard makes typing the scheme tedious. */
    private static String normalize(String raw) {
        String v = raw.trim();
        if (v.isEmpty()) {
            return "";
        }
        if (!v.contains("://")) {
            v = "http://" + v;
        }
        while (v.endsWith("/")) {
            v = v.substring(0, v.length() - 1);
        }
        return v;
    }

    private static String hostPort(String url) {
        Uri u = Uri.parse(url);
        return u.getHost() + ":" + u.getPort();
    }

    // ---------- menu ----------

    @Override
    public boolean onCreateOptionsMenu(Menu menu) {
        menu.add(0, 1, 0, "Home");
        menu.add(0, 2, 1, "Reload");
        menu.add(0, 3, 2, "Server…");
        return true;
    }

    @Override
    public boolean onOptionsItemSelected(MenuItem item) {
        switch (item.getItemId()) {
            case 1:
                String url = serverUrl();
                if (url != null) {
                    web.loadUrl(url);
                }
                return true;
            case 2:
                web.reload();
                return true;
            case 3:
                askServer(false);
                return true;
        }
        return super.onOptionsItemSelected(item);
    }

    // ---------- navigation ----------

    /**
     * Back out of fullscreen video. Handled here rather than only in
     * onBackPressed so the key is claimed before the fullscreen view (which is
     * Chromium's own, not ours) gets a chance to swallow it.
     *
     * Worth knowing when testing this: the first time an app goes immersive,
     * the system puts up its own "Viewing full screen" window, and that window
     * takes focus — Back goes to it, not to the app, until it is dismissed.
     * That is the platform's behaviour, not a bug in this activity.
     */
    @Override
    public boolean dispatchKeyEvent(KeyEvent ev) {
        if (ev.getKeyCode() == KeyEvent.KEYCODE_BACK && customView != null) {
            if (ev.getAction() == KeyEvent.ACTION_UP) {
                web.getWebChromeClient().onHideCustomView();
            }
            return true; // swallow the down event too, or the view still sees it
        }
        return super.dispatchKeyEvent(ev);
    }

    @Override
    public void onBackPressed() {
        if (customView != null) {
            web.getWebChromeClient().onHideCustomView();
            return;
        }
        // The browser and player are two separate pages by design, so Back is
        // how you get from a video back to the file list.
        if (web.canGoBack()) {
            web.goBack();
            return;
        }
        super.onBackPressed();
    }

    @Override
    protected void onSaveInstanceState(Bundle out) {
        super.onSaveInstanceState(out);
        web.saveState(out);
    }

    @Override
    protected void onPause() {
        super.onPause();
        web.onPause();
    }

    @Override
    protected void onResume() {
        super.onResume();
        web.onResume();
    }

    @Override
    protected void onDestroy() {
        // Tell the server to drop the transcode session instead of leaving it
        // to the 10-minute idle reaper.
        web.loadUrl("about:blank");
        super.onDestroy();
    }

    // ---------- WebView clients ----------

    private class Client extends WebViewClient {
        @Override
        public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest req) {
            String url = req.getUrl().toString();
            String server = serverUrl();
            if (server != null && hostPort(url).equals(hostPort(server))) {
                return false; // our own pages stay in the app
            }
            // Anything else is a link out; hand it to the system browser
            // rather than turning this WebView into a general-purpose one.
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, req.getUrl()));
            } catch (Exception ignored) {
            }
            return true;
        }

        @Override
        public void onPageFinished(WebView view, String url) {
            view.evaluateJavascript(KEEP_AWAKE_JS, null);
        }

        @Override
        public void onReceivedError(WebView view, WebResourceRequest req, WebResourceError err) {
            if (!req.isForMainFrame()) {
                return; // a failed thumbnail must not throw up a dialog
            }
            new AlertDialog.Builder(MainActivity.this)
                    .setTitle("Can't reach the server")
                    .setMessage(serverUrl() + "\n\nIs it running, and is the phone on the same network?")
                    .setPositiveButton("Retry", new DialogInterface.OnClickListener() {
                        @Override
                        public void onClick(DialogInterface d, int w) {
                            String url = serverUrl();
                            if (url != null) {
                                web.loadUrl(url);
                            }
                        }
                    })
                    .setNegativeButton("Server…", new DialogInterface.OnClickListener() {
                        @Override
                        public void onClick(DialogInterface d, int w) {
                            askServer(false);
                        }
                    })
                    .show();
        }
    }

    /**
     * Fullscreen video. The player requests fullscreen on #stage (not the bare
     * <video>, so its OSD and shortcut card stay visible), which arrives here
     * as a custom view that has to replace the WebView for the duration.
     */
    private class Chrome extends WebChromeClient {
        @Override
        public void onShowCustomView(View view, CustomViewCallback callback) {
            if (customView != null) {
                callback.onCustomViewHidden();
                return;
            }
            customView = view;
            customViewCallback = callback;
            web.setVisibility(View.GONE);
            root.addView(view, new FrameLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
            if (getActionBar() != null) {
                getActionBar().hide();
            }
            // Video is landscape, phones are held portrait. Rotating on the
            // way in is what a media app is expected to do; SENSOR_LANDSCAPE
            // rather than LANDSCAPE so both ways up still work.
            setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE);
            View decor = getWindow().getDecorView();
            savedSystemUi = decor.getSystemUiVisibility();
            decor.setSystemUiVisibility(
                    View.SYSTEM_UI_FLAG_FULLSCREEN
                            | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                            | View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
                            | View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                            | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
                            | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION);
            getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        }

        @Override
        public void onHideCustomView() {
            if (customView == null) {
                return;
            }
            root.removeView(customView);
            customView = null;
            web.setVisibility(View.VISIBLE);
            setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_USER);
            if (getActionBar() != null) {
                getActionBar().show();
            }
            getWindow().getDecorView().setSystemUiVisibility(savedSystemUi);
            if (customViewCallback != null) {
                customViewCallback.onCustomViewHidden();
                customViewCallback = null;
            }
        }
    }

    /**
     * Bridged to the page as MPHost. Only ever reached from the configured
     * server's own pages: shouldOverrideUrlLoading sends every other origin to
     * the system browser.
     */
    private class Bridge {
        @JavascriptInterface
        public void awake(final boolean on) {
            runOnUiThread(new Runnable() {
                @Override
                public void run() {
                    if (on) {
                        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
                    } else if (customView == null) {
                        getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
                    }
                }
            });
        }
    }
}
