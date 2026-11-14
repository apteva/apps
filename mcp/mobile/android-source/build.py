"""Rebuild the bounded Android UI source and secure input helpers (JDK + Android SDK)."""
import argparse, pathlib, subprocess, tempfile, zipfile
p=argparse.ArgumentParser();p.add_argument('--sdk',type=pathlib.Path,required=True);p.add_argument('--platform',default='android-35');p.add_argument('--build-tools',default='36.1.0');args=p.parse_args()
root=pathlib.Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix='apteva-ui-source-') as directory:
 temp=pathlib.Path(directory)
 subprocess.run(['javac','--release','8','-cp',str(args.sdk/'platforms'/args.platform/'android.jar'),'-d',str(temp),*[str(f) for f in sorted(root.glob('*.java'))]],check=True)
 subprocess.run([str(args.sdk/'build-tools'/args.build_tools/'d8'),'--min-api','26','--output',str(temp),*[str(f) for f in sorted(temp.glob('*.class'))]],check=True)
 with zipfile.ZipFile(root/'source.dex.jar','w') as jar:
  info=zipfile.ZipInfo('classes.dex',(2026,1,1,0,0,0));info.compress_type=zipfile.ZIP_DEFLATED
  jar.writestr(info,(temp/'classes.dex').read_bytes())
