"""Bounded local CPU pose inference. No external inference or uploads."""
import os
os.environ.setdefault('OMP_NUM_THREADS','2')
os.environ.setdefault('OPENBLAS_NUM_THREADS','2')
import sys,json,math,time,subprocess,tempfile,hashlib
import numpy as np
import mediapipe as mp
THRESHOLD=0.5
RATIO=9/16
req=json.load(open(sys.argv[1]))
def bounds(points):
    return [min(p[0] for p in points),min(p[1] for p in points),max(p[0] for p in points),max(p[1] for p in points)]

def midpoint(a,b): return (a+b)/2

def plan_pose(points,w,h,ratio=RATIO):
    good=lambda i: points[i][2]>=THRESHOLD
    face=[points[i][:2] for i in range(11) if good(i)]
    if len(face)<3 or not (good(11) and good(12)):
        return {'status':'insufficient_head_or_shoulder_evidence'}
    face=np.array(face)
    shoulder_span=float(np.linalg.norm(points[11,:2]-points[12,:2]))
    face_span=max(float(np.ptp(face[:,0])),float(np.ptp(face[:,1])))
    # Pose landmarks do not delineate hair/scalp. Use a conservative head
    # margin based on face span and shoulder scale, including reclining heads.
    head_pad=max(20*w/1920,face_span*0.60,shoulder_span*0.19)
    fb=bounds(face)
    hb=[fb[0]-head_pad,fb[1]-head_pad,fb[2]+head_pad,fb[3]+head_pad]
    # Eyes/ears alone exclude the scalp and tied-up hair. Extrapolate from
    # mouth through eyes in the head's own orientation, including reclining
    # and downward-facing poses; reserve a small area around that estimate.
    if all(good(i) for i in (2,5,9,10)):
        eye_center=midpoint(points[2,:2],points[5,:2])
        mouth_center=midpoint(points[9,:2],points[10,:2])
        scalp=eye_center+2.5*(eye_center-mouth_center)
        scalp_pad=max(20*w/1920,face_span*0.25)
        hb=[min(hb[0],scalp[0]-scalp_pad),min(hb[1],scalp[1]-scalp_pad),max(hb[2],scalp[0]+scalp_pad),max(hb[3],scalp[1]+scalp_pad)]
    required=[]
    for i in range(11,23):
        if good(i): required.append((i,points[i,:2]))
    # Pose fingertips are approximate, especially with motion blur or a hand
    # meeting the source boundary. Reserve real hand area around those points.
    forearms=[float(np.linalg.norm(points[a,:2]-points[b,:2])) for a,b in [(13,15),(14,16)] if good(a) and good(b)]
    hand_pad=max(28*w/1920,shoulder_span*0.14,max(forearms,default=0)*0.14)
    req_points=[np.array([hb[0],hb[1]]),np.array([hb[2],hb[3]])]
    for i,p in required:
        req_points.extend([p-hand_pad,p+hand_pad])
    raw=bounds(req_points)
    source_clipped=[raw[0]<0,raw[1]<0,raw[2]>w,raw[3]>h]
    bb=[max(0,raw[0]),max(0,raw[1]),min(w,raw[2]),min(h,raw[3])]
    bw,bh=bb[2]-bb[0],bb[3]-bb[1]
    max_h=min(h,w/ratio)
    max_w=max_h*ratio
    width_fits=bw<=max_w
    height_fits=bh<=max_h
    base_h=max(bh/0.92,bw/ratio/0.94,h*0.63)
    ch=min(max_h,base_h)
    cw=ch*ratio
    if not width_fits or not height_fits:
        ch=max_h; cw=max_w
    # Favor the head when an impossible crop must be shown for diagnosis.
    face_center=np.mean(face,axis=0)
    preferred_x=float(face_center[0]*0.6+((bb[0]+bb[2])/2)*0.4-cw/2)
    lo=max(0,bb[2]-cw); hi=min(w-cw,bb[0])
    x=float(np.clip(preferred_x,lo,hi)) if lo<=hi else float(np.clip(preferred_x,0,w-cw))
    preferred_y=bb[1]-ch*0.055
    lo=max(0,bb[3]-ch); hi=min(h-ch,bb[1])
    y=float(np.clip(preferred_y,lo,hi)) if lo<=hi else float(np.clip(preferred_y,0,h-ch))
    # Even dimensions preserve chroma alignment for video encoders.
    unit_w,unit_h=req['ratio_w'],req['ratio_h']
    divisor=math.gcd(unit_w,unit_h);unit_w//=divisor;unit_h//=divisor
    if unit_w%2 or unit_h%2:unit_w*=2;unit_h*=2
    unit=max(1,min(int(ch/unit_h),int(cw/unit_w)))
    iw=unit*unit_w;ih=unit*unit_h
    if width_fits and height_fits:
        # Rounding must not destroy feasible coverage: increase one unit if
        # necessary and still inside the source.
        if (iw<bw or ih<bh) and (unit+1)*unit_h<=h and (unit+1)*unit_w<=w:
            unit+=1; iw=unit*unit_w; ih=unit*unit_h
        lo=max(0,math.ceil(bb[2]-iw)); hi=min(w-iw,math.floor(bb[0]))
        x=int(np.clip(round(x),lo,hi)) if lo<=hi else int(np.clip(round(x),0,w-iw))
        lo=max(0,math.ceil(bb[3]-ih)); hi=min(h-ih,math.floor(bb[1]))
        y=int(np.clip(round(y),lo,hi)) if lo<=hi else int(np.clip(round(y),0,h-ih))
    else:
        x=int(np.clip(round(x),0,w-iw)); y=int(np.clip(round(y),0,h-ih))
    fits=(x<=bb[0] and y<=bb[1] and x+iw>=bb[2] and y+ih>=bb[3])
    outside=[i for i,p in required if not(x<=p[0]<x+iw and y<=p[1]<y+ih)]
    weak_hands=[i for i in [15,16] if not good(i)]
    return {'status':'fits_detected_upper_pose' if fits and not weak_hands else ('uncertain_hand_evidence' if fits else 'upper_pose_exceeds_crop'),
        'crop':{'x':x,'y':y,'width':iw,'height':ih},'ratio':str(req['ratio_w'])+':'+str(req['ratio_h']),
        'required_bounds':bb,'head_bounds_estimate':hb,'required_landmark_indices':[i for i,p in required],
        'required_landmarks_outside_crop':outside,'low_confidence_wrist_indices':weak_hands,
        'subject_extent_clipped_by_source':source_clipped,
        'max_possible_crop_width':max_w,'required_upper_pose_width':bw,
        'head_margin_pixels':head_pad,'hand_margin_pixels':hand_pad,
        'validation_scope':'Model landmarks and estimated head geometry only; requires visual review. Full body and legs are not required by this portrait experiment.'}


class PoseRuntimeFailure(Exception):
    def __init__(self,code,at_ms=None,attempts=0):
        self.code=code;self.at_ms=at_ms;self.attempts=attempts

def extract_frame(req,at,path,deadline):
    # A failed or empty seek must never reuse the preceding frame.
    for attempt in range(1,4):
        if time.monotonic()>=deadline:
            raise PoseRuntimeFailure('pose_source_read_timeout',at,attempt-1)
        if os.path.exists(path):os.unlink(path)
        args=[req['ffmpeg'],'-nostdin','-y','-loglevel','error','-threads','2','-ss',str(at/1000),'-i',req['source'],'-frames:v','1','-threads','2',path]
        try:
            run=subprocess.run(args,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,timeout=min(15,max(.01,deadline-time.monotonic())))
            if run.returncode==0 and os.path.isfile(path) and os.path.getsize(path)>0:
                return attempt
        except subprocess.TimeoutExpired:
            pass
        if attempt<3:
            time.sleep(min(.2*attempt,max(0,deadline-time.monotonic())))
    code='pose_source_read_timeout' if time.monotonic()>=deadline else 'pose_source_frame_unavailable'
    raise PoseRuntimeFailure(code,at,3)

def main():
    if mp.__version__!='0.10.21':raise RuntimeError('pose_runtime_version_mismatch')
    if hashlib.sha256(open(req['model'],'rb').read()).hexdigest()!=req['model_sha256']:raise RuntimeError('pose_model_hash_mismatch')
    if not 0<len(req['positions'])<=256:raise RuntimeError('pose_sample_budget_exceeded')
    deadline=time.monotonic()+min(120,float(req['remaining_seconds']))
    options=mp.tasks.vision.PoseLandmarkerOptions(base_options=mp.tasks.BaseOptions(model_asset_path=req['model'],delegate=mp.tasks.BaseOptions.Delegate.CPU),running_mode=mp.tasks.vision.RunningMode.VIDEO if req["video"] else mp.tasks.vision.RunningMode.IMAGE,num_poses=1,min_pose_detection_confidence=.5,min_pose_presence_confidence=.5)
    samples=[]
    with tempfile.TemporaryDirectory(prefix='media-pose-frames-') as work,mp.tasks.vision.PoseLandmarker.create_from_options(options) as detector:
        for at in req['positions']:
            path=os.path.join(work,'frame.png')
            attempts=extract_frame(req,at,path,deadline)
            image=mp.Image.create_from_file(path)
            if image.width!=req['width'] or image.height!=req['height']:raise RuntimeError('pose_source_geometry_mismatch')
            started=time.monotonic();result=detector.detect_for_video(image,at) if req["video"] else detector.detect(image)
            sample={'extraction_attempts':attempts,'at_ms':at,'inference_ms':(time.monotonic()-started)*1000,'status':'no_pose_detected'}
            if result.pose_landmarks:
                p=np.array([[lm.x*image.width,lm.y*image.height,min(lm.visibility,lm.presence)] for lm in result.pose_landmarks[0]])
                sample.update(plan_pose(p,image.width,image.height,req['ratio_w']/req['ratio_h']))
            samples.append(sample)
    print('APTEVA_POSE:'+json.dumps({'samples':samples,'runtime':'mediapipe-0.10.21','model_sha256':req['model_sha256']},default=lambda v:v.item() if isinstance(v,np.generic) else str(v)))
if __name__=='__main__':
    try:main()
    except Exception as error:
        # Only stable, allowlisted codes and numeric context cross the boundary.
        codes={'pose_runtime_version_mismatch','pose_model_hash_mismatch','pose_sample_budget_exceeded','pose_source_geometry_mismatch'}
        failure={'code':error.code,'at_ms':error.at_ms,'attempts':error.attempts} if isinstance(error,PoseRuntimeFailure) else {'code':str(error) if str(error) in codes else 'pose_inference_failed'}
        print('APTEVA_POSE_ERROR:'+json.dumps(failure),flush=True)
        sys.exit(1)
