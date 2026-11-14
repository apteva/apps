import android.app.UiAutomation;
import android.os.HandlerThread;
import android.os.Looper;
import android.view.accessibility.AccessibilityNodeInfo;
import android.graphics.Rect;
import java.lang.reflect.Constructor;

public final class AptevaUiSource {
  static final StringBuilder out = new StringBuilder();
  static int count;
  static String esc(CharSequence v) {
    if (v == null) return "";
    String s=v.toString();
    if(s.length()>16384) s=s.substring(0,16384);
    StringBuilder b=new StringBuilder();
    for(int i=0;i<s.length();i++) {char c=s.charAt(i); if(c=='&')b.append("&amp;");else if(c=='<')b.append("&lt;");else if(c=='\"')b.append("&quot;");else if(c=='\n')b.append("&#10;");else if(c=='\r')b.append("&#13;");else if(c=='\t')b.append("&#9;");else if(c>=32)b.append(c);}
    return b.toString();
  }
  static void attr(String k, Object v) {out.append(' ').append(k).append("=\"").append(esc(v==null?null:v.toString())).append('"');}
  static void node(AccessibilityNodeInfo n,int depth) {
    if(n==null)return;
    if(depth>80 || ++count>12000 || out.length()>3500000) throw new IllegalStateException("UI source exceeds bounds");
    Rect r=new Rect();n.getBoundsInScreen(r);
    out.append("<node");
    attr("package",n.getPackageName());attr("resource-id",n.getViewIdResourceName());attr("class",n.getClassName());
    attr("text",n.isPassword()?"":n.getText());attr("content-desc",n.isPassword()?"":n.getContentDescription());
    attr("clickable",n.isClickable());attr("enabled",n.isEnabled());attr("focused",n.isFocused());attr("password",n.isPassword());attr("visible-to-user",n.isVisibleToUser());
    attr("bounds","["+r.left+","+r.top+"]["+r.right+","+r.bottom+"]");out.append('>');
    for(int i=0;i<n.getChildCount();i++){AccessibilityNodeInfo c=n.getChild(i);if(c!=null){node(c,depth+1);c.recycle();}}
    out.append("</node>");
  }
  public static void main(String[] args) {
    HandlerThread thread=new HandlerThread("apteva-ui-source");thread.start();Object automation=null;int status=0;
    try {
      Class<?> connectionType=Class.forName("android.app.IUiAutomationConnection");
      Object connection=Class.forName("android.app.UiAutomationConnection").getConstructor().newInstance();
      Constructor<?> ctor=UiAutomation.class.getConstructor(Looper.class,connectionType);
      automation=ctor.newInstance(thread.getLooper(),connection);
      UiAutomation.class.getMethod("connect",int.class).invoke(automation,1);
      AccessibilityNodeInfo root=(AccessibilityNodeInfo)UiAutomation.class.getMethod("getRootInActiveWindow").invoke(automation);
      for(int i=0;root==null && i<20;i++){Thread.sleep(100);root=(AccessibilityNodeInfo)UiAutomation.class.getMethod("getRootInActiveWindow").invoke(automation);}
      if(root==null)throw new IllegalStateException("No active UI root");
      out.append("<?xml version=\"1.0\" encoding=\"UTF-8\"?><hierarchy rotation=\"0\">");node(root,0);out.append("</hierarchy>");root.recycle();
      System.out.print(out.toString());System.out.flush();
    } catch(Throwable error) { System.err.println("Apteva UI source unavailable: "+error.getClass().getSimpleName());status=1; }
    finally { if(automation!=null)try{UiAutomation.class.getMethod("disconnect").invoke(automation);}catch(Throwable ignored){} thread.quitSafely(); }
    System.exit(status);
  }
}
