"""Bounded local CPU pose inference. No external inference or uploads."""
import os
os.environ['OMP_NUM_THREADS']='1'
os.environ['OPENBLAS_NUM_THREADS']='1'
os.environ['MKL_NUM_THREADS']='1'
import sys,json,math,time,subprocess,tempfile,hashlib,contextlib
runtime_started=time.monotonic()
import numpy as np
import mediapipe as mp
THRESHOLD=0.5
RATIO=9/16
req=json.load(open(sys.argv[1]))
timings={'import_ms':(time.monotonic()-runtime_started)*1000,'model_setup_ms':0,'frame_extraction_ms':0,'pose_inference_ms':0,'recovery_ms':0,'total_ms':0,'budget_seconds':float(req['remaining_seconds']),'source_kind':'remote_url' if req['source'].startswith(('http://','https://')) else 'local_disk','failure_stage':'initialization'}
report={'samples':[],'runtime':'mediapipe-'+mp.__version__,'model_sha256':'','timings':timings}
def report_payload():
    timings['total_ms']=(time.monotonic()-runtime_started)*1000
    return report
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
    if not width_fits or not height_fits or req.get('framing')=='widest_valid':
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
    if req.get('framing')=='widest_valid':
        iw=2*int(max_w/2);ih=2*int(max_h/2)
    if width_fits and height_fits:
        # Rounding must not destroy feasible coverage: increase one unit if
        # necessary and still inside the source.
        if req.get('framing')!='widest_valid' and (iw<bw or ih<bh) and (unit+1)*unit_h<=h and (unit+1)*unit_w<=w:
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
        'landmark_span_width':float(np.ptp(np.vstack([face]+[p[None] for _,p in required])[:,0])),
        'validation_scope':'Model landmarks and estimated head geometry only; requires visual review. Full body and legs are not required by this portrait experiment.'}


def landmark_evidence(points,indices=range(23)):
    return [{'index':i,'x':round(float(points[i,0]),2),'y':round(float(points[i,1]),2),'visibility':round(float(points[i,2]),6),'presence':round(float(points[i,3]),6)} for i in indices]

def hand_refresh(points,fresh):
    # Refresh only a weak hand on the same native frame. Never interpolate
    # confidence or carry a hand through an occlusion from another timestamp.
    good=lambda p,i:min(p[i,2:])>=THRESHOLD
    if not all(good(p,i) for p in (points,fresh) for i in (11,12)):
        return points,[],'identity_unverified'
    span=max(30,float(np.linalg.norm(points[11,:2]-points[12,:2])))
    if any(np.linalg.norm(points[i,:2]-fresh[i,:2])>span*.25 for i in (11,12)):
        return points,[],'identity_unverified'
    face=[i for i in range(11) if good(points,i) and good(fresh,i)]
    if len(face)<3 or np.linalg.norm(np.mean(points[face,:2],axis=0)-np.mean(fresh[face,:2],axis=0))>span*.35:
        return points,[],'identity_unverified'
    merged=points.copy();accepted=[]
    for wrist,elbow,hand in [(15,13,[15,17,19,21]),(16,14,[16,18,20,22])]:
        if good(points,wrist) or not good(fresh,wrist):continue
        if not good(fresh,elbow):continue
        if good(points,elbow) and np.linalg.norm(points[elbow,:2]-fresh[elbow,:2])>span*.35:continue
        # Conflicting confident finger geometry is not evidence of recovery.
        if any(good(points,i) and good(fresh,i) and np.linalg.norm(points[i,:2]-fresh[i,:2])>span*.35 for i in hand):continue
        for i in hand:
            if good(fresh,i) and not good(points,i):merged[i]=fresh[i]
        accepted.append(wrist)
    return merged,accepted,'same_frame_hand_recovered' if accepted else 'no_additional_hand_support'

class PoseRuntimeFailure(Exception):
    def __init__(self,code,at_ms=None,attempts=0):
        self.code=code;self.at_ms=at_ms;self.attempts=attempts

def extract_frame(req,at,path,deadline):
    # A failed or empty seek must never reuse the preceding frame.
    for attempt in range(1,4):
        if time.monotonic()>=deadline:
            raise PoseRuntimeFailure('pose_source_read_timeout',at,attempt-1)
        if os.path.exists(path):os.unlink(path)
        args=[req['ffmpeg'],'-nostdin','-y','-filter_threads','1','-filter_complex_threads','1','-loglevel','error','-threads','1','-ss',str(at/1000),'-i',req['source'],'-frames:v','1','-threads','1','-compression_level','1',path]
        try:
            run=subprocess.run(args,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,timeout=min(15,max(.01,deadline-time.monotonic())))
            if run.returncode in (-9,137) or any(x in (run.stderr or b'').lower() for x in (b'cannot allocate memory',b'out of memory')):
                raise PoseRuntimeFailure('media_resource_exhausted',at,attempt)
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
    report['model_sha256']=hashlib.sha256(open(req['model'],'rb').read()).hexdigest()
    if report['model_sha256']!=req['model_sha256']:raise RuntimeError('pose_model_hash_mismatch')
    if not 0<len(req['positions'])<=256:raise RuntimeError('pose_sample_budget_exceeded')
    remaining=min(1200,float(req['remaining_seconds']))
    if req.get('expires_at'):remaining=min(remaining,max(0,req['expires_at']-time.time()))
    timings['budget_seconds']=remaining
    deadline=time.monotonic()+remaining
    options=mp.tasks.vision.PoseLandmarkerOptions(base_options=mp.tasks.BaseOptions(model_asset_path=req['model'],delegate=mp.tasks.BaseOptions.Delegate.CPU),running_mode=mp.tasks.vision.RunningMode.VIDEO if req["video"] else mp.tasks.vision.RunningMode.IMAGE,num_poses=1,min_pose_detection_confidence=.5,min_pose_presence_confidence=.5)
    samples=report['samples']
    recovery=None;recovery_unavailable=False;recovery_seconds=0;tracker_resets=0
    with tempfile.TemporaryDirectory(prefix='media-pose-frames-') as work,contextlib.ExitStack() as stack:
        timings['failure_stage']='model_setup';setup_started=time.monotonic()
        detector_stack=stack.enter_context(contextlib.ExitStack())
        detector=detector_stack.enter_context(mp.tasks.vision.PoseLandmarker.create_from_options(options))
        timings['model_setup_ms']=(time.monotonic()-setup_started)*1000
        refresh_detector=None
        for at in req['positions']:
            path=os.path.join(work,'frame.png')
            timings['failure_stage']='frame_extraction';extract_started=time.monotonic()
            try:attempts=extract_frame(req,at,path,deadline)
            finally:timings['frame_extraction_ms']+=(time.monotonic()-extract_started)*1000
            image=mp.Image.create_from_file(path)
            if time.monotonic()>=deadline:raise PoseRuntimeFailure('pose_analysis_timeout',at,attempts)
            timings['failure_stage']='pose_inference'
            if image.width!=req['width'] or image.height!=req['height']:raise RuntimeError('pose_source_geometry_mismatch')
            started=time.monotonic();result=detector.detect_for_video(image,at) if req["video"] else detector.detect(image)
            sample={'extraction_attempts':attempts,'at_ms':at,'inference_ms':(time.monotonic()-started)*1000,'status':'no_pose_detected'}
            if result.pose_landmarks:
                points=np.array([[lm.x*image.width,lm.y*image.height,lm.visibility,lm.presence] for lm in result.pose_landmarks[0]])
                to_plan=lambda v:np.column_stack((v[:,:2],np.min(v[:,2:],axis=1)))
                planned=plan_pose(to_plan(points),image.width,image.height,req['ratio_w']/req['ratio_h'])
                if not req.get('hybrid') and req['video'] and planned['status']=='uncertain_hand_evidence':
                    sample['initial_status']=planned['status']
                    sample['tracked_wrist_evidence']=landmark_evidence(points,[15,16])
                    if refresh_detector is None:
                        refresh_options=mp.tasks.vision.PoseLandmarkerOptions(base_options=mp.tasks.BaseOptions(model_asset_path=req['model'],delegate=mp.tasks.BaseOptions.Delegate.CPU),running_mode=mp.tasks.vision.RunningMode.IMAGE,num_poses=1,min_pose_detection_confidence=.5,min_pose_presence_confidence=.5)
                        refresh_detector=stack.enter_context(mp.tasks.vision.PoseLandmarker.create_from_options(refresh_options))
                    fresh=refresh_detector.detect(image)
                    sample['hand_refresh_status']='no_pose_detected'
                    if fresh.pose_landmarks:
                        new=np.array([[lm.x*image.width,lm.y*image.height,lm.visibility,lm.presence] for lm in fresh.pose_landmarks[0]])
                        points,accepted,status=hand_refresh(points,new)
                        sample['hand_refresh_status']=status
                        sample['refreshed_hand_indices']=accepted
                        planned=plan_pose(to_plan(points),image.width,image.height,req['ratio_w']/req['ratio_h'])
                sample['landmark_evidence']=landmark_evidence(points)
                sample.update(planned)
                sample['inference_ms']=(time.monotonic()-started)*1000
            timings['pose_inference_ms']+=(time.monotonic()-started)*1000
            if req.get('hybrid'):
                timings['failure_stage']='hybrid_recovery'
                recovery_start=time.monotonic()
                if recovery_unavailable:
                    sample['recovery']={'status':'runtime_unavailable','mode':'same_frame'}
                    sample['geometry_trust']='unverified';sample['status']='uncertain_person_extent';sample['extent_scope']='full_person_recovery'
                elif recovery_seconds>=30 or deadline-time.monotonic()<2:
                    sample['recovery']={'status':'budget_exhausted','mode':'same_frame'}
                    sample['geometry_trust']='unverified';sample['status']='uncertain_person_extent';sample['extent_scope']='full_person_recovery'
                else:
                    try:
                        if recovery is None:
                            from smartcrop_pose_recovery import Recovery
                            recovery=Recovery(req['recovery_root'])
                        import cv2
                        frame=cv2.imread(path)
                        def fresh_points():
                            nonlocal refresh_detector
                            if refresh_detector is None:
                                fresh_options=mp.tasks.vision.PoseLandmarkerOptions(base_options=mp.tasks.BaseOptions(model_asset_path=req['model'],delegate=mp.tasks.BaseOptions.Delegate.CPU),running_mode=mp.tasks.vision.RunningMode.IMAGE,num_poses=1,min_pose_detection_confidence=.5,min_pose_presence_confidence=.5)
                                refresh_detector=stack.enter_context(mp.tasks.vision.PoseLandmarker.create_from_options(fresh_options))
                            fresh=refresh_detector.detect(image)
                            return np.array([[lm.x*image.width,lm.y*image.height,lm.visibility,lm.presence] for lm in fresh.pose_landmarks[0]]) if fresh.pose_landmarks else None
                        sample=recovery.arbitrate(frame,sample,fresh_points,lambda v:plan_pose(np.column_stack((v[:,:2],np.min(v[:,2:],axis=1))),image.width,image.height,req['ratio_w']/req['ratio_h']),req.get('framing','upper_body'),req['ratio_w']/req['ratio_h'])
                        if req['video'] and any(i<13 for i in sample['recovery'].get('conflicting_primary_indices',[])) and tracker_resets<8:
                            detector_stack.close()
                            detector=detector_stack.enter_context(mp.tasks.vision.PoseLandmarker.create_from_options(options))
                            tracker_resets+=1
                            sample['recovery']['tracker_reset']='independent_head_or_torso_disagreement'
                            sample['recovery']['tracker_resets']=tracker_resets
                    except Exception as recovery_error:
                        if isinstance(recovery_error,MemoryError) or any(x in str(recovery_error).lower() for x in ("out of memory","cannot allocate memory","bad_alloc")):
                            raise PoseRuntimeFailure("media_resource_exhausted",at,1)
                        recovery_unavailable=True
                        code=str(recovery_error) if str(recovery_error) in {'recovery_model_hash_mismatch','recovery_runtime_version_mismatch'} else ('recovery_model_missing' if isinstance(recovery_error,FileNotFoundError) else 'recovery_inference_failed')
                        sample['recovery']={'status':'runtime_unavailable','mode':'same_frame','failure_code':code}
                        sample['geometry_trust']='unverified';sample['status']='uncertain_person_extent';sample['extent_scope']='full_person_recovery'
                    recovery_seconds+=time.monotonic()-recovery_start
                sample['recovery']['elapsed_ms']=(time.monotonic()-recovery_start)*1000
                timings['recovery_ms']+=(time.monotonic()-recovery_start)*1000
            samples.append(sample)
            if time.monotonic()>=deadline:raise PoseRuntimeFailure('pose_analysis_timeout',at,attempts)
    timings.pop('failure_stage',None)
    print('APTEVA_POSE:'+json.dumps(report_payload(),default=lambda v:v.item() if isinstance(v,np.generic) else str(v)),flush=True)
if __name__=='__main__':
    try:main()
    except Exception as error:
        # Only stable, allowlisted codes and numeric context cross the boundary.
        codes={'pose_runtime_version_mismatch','pose_model_hash_mismatch','pose_sample_budget_exceeded','pose_source_geometry_mismatch'}
        failure={'code':error.code,'at_ms':error.at_ms,'attempts':error.attempts} if isinstance(error,PoseRuntimeFailure) else {'code':'media_resource_exhausted' if isinstance(error,MemoryError) else (str(error) if str(error) in codes else 'pose_inference_failed')}
        failure['partial_result']=report_payload()
        print('APTEVA_POSE_ERROR:'+json.dumps(failure,default=lambda v:v.item() if isinstance(v,np.generic) else str(v)),flush=True)
        sys.exit(1)
