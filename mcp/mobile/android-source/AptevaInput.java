import android.app.UiAutomation;
import android.os.Bundle;
import android.os.HandlerThread;
import android.os.Looper;
import android.os.SystemClock;
import android.view.InputDevice;
import android.view.MotionEvent;
import android.view.accessibility.AccessibilityNodeInfo;
import java.io.ByteArrayOutputStream;
import java.lang.reflect.Constructor;
import org.json.JSONArray;
import org.json.JSONObject;

// Input enters through stdin as one JSON object, never argv. Ops:
//   {"op":"gesture","points":[[x,y],...],"duration_ms":n}  one finger, pixels
//   {"op":"set_text","text":"..."}  ACTION_SET_TEXT on the focused regular field
// A gesture interrupted by SIGTERM (Mobile's stop) is cancelled, so no finger
// stays down. Output is a fixed receipt; text is never printed.
public final class AptevaInput {
  static UiAutomation ui;
  static volatile boolean down;
  static volatile long downTime;
  static volatile float lastX, lastY;

  static boolean inject(int action, float x, float y, long downAt) {
    MotionEvent ev = MotionEvent.obtain(downAt, SystemClock.uptimeMillis(), action, x, y, 0);
    ev.setSource(InputDevice.SOURCE_TOUCHSCREEN);
    try { return ui.injectInputEvent(ev, true); } finally { ev.recycle(); }
  }

  static void gesture(JSONObject in) throws Exception {
    JSONArray pts = in.getJSONArray("points");
    int n = pts.length();
    if (n < 1 || n > 64) throw new Exception();
    long duration = in.optLong("duration_ms", 100);
    if (duration < 0 || duration > 10000) throw new Exception();
    float[] xs = new float[n], ys = new float[n];
    for (int i = 0; i < n; i++) { JSONArray p = pts.getJSONArray(i); xs[i] = (float) p.getDouble(0); ys[i] = (float) p.getDouble(1); }
    downTime = SystemClock.uptimeMillis();
    lastX = xs[0]; lastY = ys[0];
    if (!inject(MotionEvent.ACTION_DOWN, xs[0], ys[0], downTime)) throw new Exception();
    down = true;
    // Spread the moves over the duration in ~10 ms steps along the path.
    int steps = (int) Math.max(1, duration / 10);
    long start = SystemClock.uptimeMillis();
    for (int s = 1; s <= steps; s++) {
      float t = (float) s / steps * (n - 1);
      int i = Math.min((int) t, n - 2 < 0 ? 0 : n - 2);
      float f = n == 1 ? 0 : t - i;
      float x = n == 1 ? xs[0] : xs[i] + (xs[i + 1] - xs[i]) * f;
      float y = n == 1 ? ys[0] : ys[i] + (ys[i + 1] - ys[i]) * f;
      long due = start + duration * s / steps;
      long wait = due - SystemClock.uptimeMillis();
      if (wait > 0) Thread.sleep(wait);
      if (n > 1 && (x != lastX || y != lastY)) { inject(MotionEvent.ACTION_MOVE, x, y, downTime); lastX = x; lastY = y; }
    }
    inject(MotionEvent.ACTION_UP, xs[n - 1], ys[n - 1], downTime);
    down = false;
  }

  static void setText(JSONObject in) throws Exception {
    String text = in.getString("text");
    if (text.length() > 4096) throw new Exception();
    AccessibilityNodeInfo root = ui.getRootInActiveWindow();
    for (int i = 0; root == null && i < 20; i++) { Thread.sleep(100); root = ui.getRootInActiveWindow(); }
    if (root == null) throw new Exception();
    AccessibilityNodeInfo field = root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT);
    if (field == null || field.isPassword() || !field.isEditable()) throw new Exception();
    Bundle args = new Bundle();
    args.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text);
    if (!field.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)) throw new Exception();
  }

  public static void main(String[] argv) {
    HandlerThread thread = new HandlerThread("apteva-input"); thread.start(); int status = 1;
    Runtime.getRuntime().addShutdownHook(new Thread(() -> {
      if (down && ui != null) { try { inject(MotionEvent.ACTION_CANCEL, lastX, lastY, downTime); } catch (Throwable ignored) {} down = false; }
    }));
    try {
      ByteArrayOutputStream bytes = new ByteArrayOutputStream(); byte[] chunk = new byte[1024]; int r;
      while ((r = System.in.read(chunk)) != -1) { bytes.write(chunk, 0, r); if (bytes.size() > 65536) throw new Exception(); }
      JSONObject in = new JSONObject(new String(bytes.toByteArray(), "UTF-8"));
      Class<?> connectionType = Class.forName("android.app.IUiAutomationConnection");
      Object connection = Class.forName("android.app.UiAutomationConnection").getConstructor().newInstance();
      Constructor<?> ctor = UiAutomation.class.getConstructor(Looper.class, connectionType);
      ui = (UiAutomation) ctor.newInstance(thread.getLooper(), connection);
      UiAutomation.class.getMethod("connect", int.class).invoke(ui, 1);
      String op = in.getString("op");
      if (op.equals("gesture")) gesture(in);
      else if (op.equals("set_text")) setText(in);
      else throw new Exception();
      System.out.print("{\"ok\":true}"); System.out.flush(); status = 0;
    } catch (Throwable error) {
      System.err.print("Apteva input unavailable: " + error.getClass().getSimpleName());
    } finally {
      if (down && ui != null) { try { inject(MotionEvent.ACTION_CANCEL, lastX, lastY, downTime); } catch (Throwable ignored) {} down = false; }
      if (ui != null) try { UiAutomation.class.getMethod("disconnect").invoke(ui); } catch (Throwable ignored) {}
      thread.quitSafely();
    }
    System.exit(status);
  }
}
