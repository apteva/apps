# Smart Crop recovery models

Recovery runs on the same source frame, on CPU. MediaPipe Full remains the
primary estimator. Model scores from different estimators are not equivalent
visibility probabilities. Coarse person extents and unresolved gaps cannot
establish action coverage.

## YOLO11n Pose

- Original weights: https://github.com/ultralytics/assets/releases/download/v8.3.0/yolo11n-pose.pt
- Upstream: https://github.com/ultralytics/ultralytics/tree/v8.3.98
- License: AGPL-3.0, https://github.com/ultralytics/ultralytics/blob/v8.3.98/LICENSE
- Export: Python 3.11, torch 2.6.0, Ultralytics 8.3.98, ONNX 1.17.0.
- Export options: format=onnx, imgsz=640, dynamic=True, simplify=False, opset=17.
- SHA256: eb9543a3f625fc19e7d6134cf33e801542a81b34a15aaa9bdc25cdd1ec741309
- Unmodified pretrained weights; no retraining. ONNX inference is separate from
  the Go app, in the isolated CPU Python worker. Ultralytics is only used for
  reproducible model export, not installed in the production runtime.

## RTMPose M, Halpe26

- Upstream: https://github.com/open-mmlab/mmpose/tree/main/projects/rtmpose
- License: Apache-2.0, https://github.com/open-mmlab/mmpose/blob/main/LICENSE
- Official SDK export: https://download.openmmlab.com/mmpose/v1/projects/rtmposev1/onnx_sdk/rtmpose-m_simcc-body7_pt-body7-halpe26_700e-256x192-4d3e73dd_20230605.zip
- SDK archive SHA256: 55b81170e236040b59fc792ad0a8315301ac4c079a3bdb1095d838aad3088d18
- Extracted end2end.onnx SHA256: 26f3a19e61304a600dfb82d1001d41d24343b89fc70a33ffc84657e0b0bf2ecf
- Input: RGB, 192x256, mean (123.675,116.28,103.53), std
  (58.395,57.12,57.375). SimCC split ratio 2; no dark refinement or flip test.
- Unmodified official export; downloaded once into the verified runtime cache.

No private source pixels or report assets are included in these model files.
