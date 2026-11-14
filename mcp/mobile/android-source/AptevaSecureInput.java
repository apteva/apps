import android.app.UiAutomation;
import android.os.Bundle;
import android.os.HandlerThread;
import android.os.Looper;
import android.graphics.Rect;
import android.view.accessibility.AccessibilityNodeInfo;
import java.io.ByteArrayOutputStream;
import java.lang.reflect.Constructor;
import java.util.ArrayList;
import org.json.JSONArray;
import org.json.JSONObject;

// Passwords enter only through stdin. Never print input, exception messages,
// node contents, or lengths. No clipboard, IME changes, files, or argv secrets.
public final class AptevaSecureInput {
  static final ArrayList<AccessibilityNodeInfo> nodes = new ArrayList<>();
  static void collect(AccessibilityNodeInfo n,int depth) throws Exception {
    if(n==null)return;
    if(depth>80 || nodes.size()>=12000)throw new Exception();
    nodes.add(n);
    for(int i=0;i<n.getChildCount();i++)collect(n.getChild(i),depth+1);
  }
  static String text(CharSequence s){return s==null?"":s.toString();}
  static boolean visible(AccessibilityNodeInfo n){return n.isVisibleToUser() && n.isEnabled();}
  static boolean matches(AccessibilityNodeInfo n,String s){return s.equals(n.getViewIdResourceName()) || (!n.isPassword() && (s.equals(text(n.getText())) || s.equals(text(n.getContentDescription()))));}
  static AccessibilityNodeInfo unique(String s) throws Exception {
    AccessibilityNodeInfo found=null;
    for(AccessibilityNodeInfo n:nodes)if(visible(n)&&matches(n,s)){if(found!=null)throw new Exception();found=n;}
    if(found==null)throw new Exception();return found;
  }
  static AccessibilityNodeInfo check(UiAutomation ui,JSONObject in,boolean before) throws Exception {
    for(AccessibilityNodeInfo n:nodes)n.recycle();nodes.clear();
    AccessibilityNodeInfo root=ui.getRootInActiveWindow();
    for(int i=0;root==null && i<20;i++){Thread.sleep(100);root=ui.getRootInActiveWindow();}
    String pkg=in.getString("expected_package");
    if(root==null || !pkg.equals(text(root.getPackageName())))throw new Exception();
    collect(root,0);
    JSONArray markers=in.getJSONArray("native_required");
    for(int i=0;i<markers.length();i++){
      boolean found=false;for(AccessibilityNodeInfo n:nodes)if(visible(n)&&matches(n,markers.getString(i)))found=true;
      if(!found)throw new Exception();
    }
    JSONObject values=in.getJSONObject("native_values");
    java.util.Iterator<String> keys=values.keys();
    while(keys.hasNext()){String k=keys.next();AccessibilityNodeInfo n=unique(k);if(n.isPassword() || !values.getString(k).equals(text(n.getText())))throw new Exception();}
    AccessibilityNodeInfo field=null;
    for(AccessibilityNodeInfo n:nodes)if(n.isFocused()){
      if(field!=null)throw new Exception();field=n;
    }
    if(field==null || !visible(field) || !field.isPassword() || !field.isEditable() || !pkg.equals(text(field.getPackageName())) || !"android.widget.EditText".equals(text(field.getClassName())))throw new Exception();
    Rect r=new Rect();field.getBoundsInScreen(r);
    String bounds="["+r.left+","+r.top+"]["+r.right+","+r.bottom+"]";
    if(!bounds.equals(in.getString("expected_bounds")) || !in.optString("expected_resource_id").equals(text(field.getViewIdResourceName())))throw new Exception();
    if(before && !field.isShowingHintText() && !text(field.getText()).isEmpty())throw new Exception();
    return field;
  }
  public static void main(String[] args){
    HandlerThread thread=new HandlerThread("apteva-secure-input");thread.start();UiAutomation ui=null;int status=1;
    try{
      ByteArrayOutputStream bytes=new ByteArrayOutputStream();byte[] chunk=new byte[1024];int n;
      while((n=System.in.read(chunk))!=-1){bytes.write(chunk,0,n);if(bytes.size()>16384)throw new Exception();}
      JSONObject in=new JSONObject(new String(bytes.toByteArray(),"UTF-8"));String password=in.getString("password");
      if(password.isEmpty() || password.length()>1024 || password.indexOf('\n')>=0 || password.indexOf('\r')>=0)throw new Exception();
      JSONArray markers=in.getJSONArray("native_required");JSONObject values=in.getJSONObject("native_values");
      if(markers.length()<1 || markers.length()>12 || values.length()<1 || values.length()>12)throw new Exception();
      Class<?> connectionType=Class.forName("android.app.IUiAutomationConnection");
      Object connection=Class.forName("android.app.UiAutomationConnection").getConstructor().newInstance();
      Constructor<?> ctor=UiAutomation.class.getConstructor(Looper.class,connectionType);
      ui=(UiAutomation)ctor.newInstance(thread.getLooper(),connection);
      UiAutomation.class.getMethod("connect",int.class).invoke(ui,1);
      AccessibilityNodeInfo field=check(ui,in,true);
      Bundle arguments=new Bundle();arguments.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,password);
      if(!field.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT,arguments))throw new Exception();
      boolean verified=false;
      for(int i=0;i<10 && !verified;i++){
        Thread.sleep(100);field=check(ui,in,false);String actual=text(field.getText());
        verified=actual.equals(password);
        if(!verified && actual.length()==password.length()){
          verified=true;for(int j=0;j<actual.length();j++)if(actual.charAt(j)!='\u2022' && actual.charAt(j)!='*' && actual.charAt(j)!='\u25cf')verified=false;
        }
      }
      if(!verified)throw new Exception();
      System.out.print("{\"secure_input\":true,\"verified\":true}");System.out.flush();status=0;
    }catch(Throwable ignored){System.err.print("Secure input unavailable; inspect privately before retrying");}
    finally{for(AccessibilityNodeInfo n:nodes)n.recycle();if(ui!=null)try{UiAutomation.class.getMethod("disconnect").invoke(ui);}catch(Throwable ignored){}thread.quitSafely();}
    System.exit(status);
  }
}
