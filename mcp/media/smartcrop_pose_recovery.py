"""Bounded, independent same-frame pose recovery. CPU only; native coordinates."""

import cv2, numpy as np, hashlib, math

PERSON_SHA = "eb9543a3f625fc19e7d6134cf33e801542a81b34a15aaa9bdc25cdd1ec741309"
RTM_SHA = "26f3a19e61304a600dfb82d1001d41d24343b89fc70a33ffc84657e0b0bf2ecf"


def overlap(a, b):
    x0 = max(a[0], b[0])
    y0 = max(a[1], b[1])
    x1 = min(a[2], b[2])
    y1 = min(a[3], b[3])
    inter = max(0, x1 - x0) * max(0, y1 - y0)
    return inter / max(
        1, (a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter
    )


def box_plan(b, head, w, h, ratio, framing):
    b = np.clip(b, [0, 0, 0, 0], [w, h, w, h]).tolist()
    bw = b[2] - b[0]
    bh = b[3] - b[1]
    max_h = min(h, w / ratio)
    max_w = max_h * ratio
    if head:
        head = np.clip(head, [0, 0, 0, 0], [w, h, w, h]).tolist()
        if head[2] <= head[0] or head[3] <= head[1]:
            head = None
    ch = min(max_h, max(bh / 0.92, bw / ratio / 0.94, h * 0.63))
    cw = ch * ratio
    if framing == "widest_valid":
        ch = max_h
        cw = max_w
    iw = min(2 * int(max_w / 2), 2 * int(math.ceil(cw / 2)))
    ih = min(2 * int(max_h / 2), 2 * int(math.ceil(ch / 2)))
    preferred = (
        ((head[0] + head[2]) / 2 * 0.6 + (b[0] + b[2]) / 2 * 0.4)
        if head
        else (b[0] + b[2]) / 2
    )
    lo = max(0, b[2] - iw)
    hi = min(w - iw, b[0])
    x = (
        float(np.clip(preferred - iw / 2, lo, hi))
        if lo <= hi
        else float(np.clip(preferred - iw / 2, 0, w - iw))
    )
    # When the full subject cannot fit, keep supported head geometry in-frame.
    if head and head[2] - head[0] <= iw:
        head_lo = max(0, head[2] - iw)
        head_hi = min(w - iw, head[0])
        if head_lo <= head_hi:
            x = float(np.clip(x, head_lo, head_hi))
    lo = max(0, b[3] - ih)
    hi = min(h - ih, b[1])
    y = (
        float(np.clip(b[1] - ih * 0.055, lo, hi))
        if lo <= hi
        else float(np.clip(b[1] - ih * 0.055, 0, h - ih))
    )
    crop = {"x": round(x), "y": round(y), "width": iw, "height": ih}
    fits = x <= b[0] and y <= b[1] and x + iw >= b[2] and y + ih >= b[3]
    return {
        "crop": crop,
        "required_bounds": b,
        "head_bounds_estimate": head or [],
        "fits_bounds": bool(fits),
    }


class Recovery:
    def __init__(self, root):
        from pathlib import Path

        self.root = Path(root)
        for name, sha in [
            ("yolo11n-pose.onnx", PERSON_SHA),
            ("rtmpose-m-halpe26.onnx", RTM_SHA),
        ]:
            if hashlib.sha256((self.root / name).read_bytes()).hexdigest() != sha:
                raise RuntimeError("recovery_model_hash_mismatch")
        import onnxruntime as ort

        if ort.__version__ != "1.22.1":
            raise RuntimeError("recovery_runtime_version_mismatch")
        opts = ort.SessionOptions()
        opts.intra_op_num_threads = 1
        opts.inter_op_num_threads = 1
        self.person = ort.InferenceSession(
            str(self.root / "yolo11n-pose.onnx"),
            opts,
            providers=["CPUExecutionProvider"],
        )
        self.pose = cv2.dnn.readNetFromONNX(str(self.root / "rtmpose-m-halpe26.onnx"))
        cv2.setNumThreads(2)

    def detect(self, im):
        h, w = im.shape[:2]
        side = 640
        scale = min(side / h, side / w)
        nw, nh = round(w * scale), round(h * scale)
        img = cv2.resize(im, (nw, nh))
        iw = int(math.ceil(nw / 32) * 32)
        ih = int(math.ceil(nh / 32) * 32)
        dx = (iw - nw) // 2
        dy = (ih - nh) // 2
        canvas = np.full((ih, iw, 3), 114, np.uint8)
        canvas[dy : dy + nh, dx : dx + nw] = img
        inp = (
            cv2.cvtColor(canvas, cv2.COLOR_BGR2RGB)
            .transpose(2, 0, 1)[None]
            .astype(np.float32)
            / 255
        )
        rows = self.person.run(None, {self.person.get_inputs()[0].name: inp})[0][0].T
        rows = rows[rows[:, 4] >= 0.5]
        boxes = []
        scores = []
        for row in rows:
            cx, cy, bw, bh = row[:4]
            boxes.append(
                [
                    float((cx - bw / 2 - dx) / scale),
                    float((cy - bh / 2 - dy) / scale),
                    float(bw / scale),
                    float(bh / scale),
                ]
            )
            scores.append(float(row[4]))
        indices = np.array(cv2.dnn.NMSBoxes(boxes, scores, 0.5, 0.45)).flatten()
        return [
            [
                boxes[int(i)][0],
                boxes[int(i)][1],
                boxes[int(i)][0] + boxes[int(i)][2],
                boxes[int(i)][1] + boxes[int(i)][3],
                scores[int(i)],
                *self.native_keypoints(rows[int(i)][5:], scale, dx, dy),
            ]
            for i in indices
        ]

    @staticmethod
    def native_keypoints(values, scale, dx, dy):
        points = np.asarray(values).reshape(17, 3).copy()
        points[:, 0] = (points[:, 0] - dx) / scale
        points[:, 1] = (points[:, 1] - dy) / scale
        return points.reshape(-1).tolist()

    @staticmethod
    def transform_keypoints(box, offset=0, flip_width=None):
        for i in range(5, len(box), 3):
            box[i] += offset
            if flip_width is not None:
                box[i] = flip_width - box[i]
        if flip_width is not None and len(box) == 56:
            points = np.asarray(box[5:]).reshape(17, 3)
            box[5:] = (
                points[[0, 2, 1, 4, 3, 6, 5, 8, 7, 10, 9, 12, 11, 14, 13, 16, 15]]
                .reshape(-1)
                .tolist()
            )

    @staticmethod
    def unrotate(box, angle, w, h):
        def point(x, y):
            if angle == 90:
                return y, h - x
            if angle == 180:
                return w - x, h - y
            return w - y, x

        corners = [point(box[x], box[y]) for x, y in [(0, 1), (0, 3), (2, 1), (2, 3)]]
        box[:4] = [
            min(p[0] for p in corners),
            min(p[1] for p in corners),
            max(p[0] for p in corners),
            max(p[1] for p in corners),
        ]
        for i in range(5, len(box), 3):
            box[i], box[i + 1] = point(box[i], box[i + 1])

    def person_box(self, im):
        h, w = im.shape[:2]
        self.detector_views = ["native"]
        self.person_view = "native"
        boxes = self.detect(im)
        if len(boxes) > 1:
            return None, "multiple_people_unresolved"
        if len(boxes) != 1 or boxes[0][4] < 0.65:
            self.detector_views.append("native_tiles")
            side = min(w, h)
            for x in sorted(set([0, (w - side) // 2, w - side])):
                for b in self.detect(im[:, x : x + side]):
                    if (x > 0 and b[0] < 4) or (x + side < w and b[2] > side - 4):
                        continue
                    b[0] += x
                    b[2] += x
                    self.transform_keypoints(b, offset=x)
                    boxes.append(b)
        if not boxes:
            self.detector_views.append("flipped")
            flip = cv2.flip(im, 1)
            for b in self.detect(flip):
                b[0], b[2] = w - b[2], w - b[0]
                self.transform_keypoints(b, flip_width=w)
                boxes.append(b)
            if not boxes:
                for x in sorted(set([0, (w - side) // 2, w - side])):
                    for b in self.detect(flip[:, x : x + side]):
                        if (x > 0 and b[0] < 4) or (x + side < w and b[2] > side - 4):
                            continue
                        b[0] += x
                        b[2] += x
                        b[0], b[2] = w - b[2], w - b[0]
                        self.transform_keypoints(b, offset=x, flip_width=w)
                        boxes.append(b)
        if not boxes:
            # Upright-trained detection can miss bent/reclining poses. Re-detect
            # bounded rotated views; all boxes and keypoints return to native
            # coordinates before same-frame identity checks. Never rotate output.
            best = -1
            for angle, mode in [
                (90, cv2.ROTATE_90_CLOCKWISE),
                (180, cv2.ROTATE_180),
                (270, cv2.ROTATE_90_COUNTERCLOCKWISE),
            ]:
                self.detector_views.append("rotated_" + str(angle))
                for b in self.detect(cv2.rotate(im, mode)):
                    self.unrotate(b, angle, w, h)
                    boxes.append(b)
                    if b[4] > best:
                        best = b[4]
                        self.person_view = "rotated_" + str(angle)
        groups = []
        for b in sorted(boxes, key=lambda b: -b[4]):
            # Merge tiled views only when overlapping geometry agrees on identity;
            # overlapping distinct people must not collapse to a single subject.
            def same_person(g):
                if overlap(g, b) <= 0.35 or len(g) != 56 or len(b) != 56:
                    return False
                a = np.asarray(g[5:]).reshape(17, 3)
                c = np.asarray(b[5:]).reshape(17, 3)
                reliable = [i for i in range(7) if min(a[i, 2], c[i, 2]) >= 0.5]
                tolerance = max(
                    45, max(g[3] - g[1], g[2] - g[0], b[3] - b[1], b[2] - b[0]) * 0.12
                )
                return len(reliable) >= 2 and all(
                    np.linalg.norm(a[i, :2] - c[i, :2]) <= tolerance for i in reliable
                )

            matches = [g for g in groups if same_person(g)]
            if matches:
                g = matches[0]
                g[:4] = [
                    min(g[0], b[0]),
                    min(g[1], b[1]),
                    max(g[2], b[2]),
                    max(g[3], b[3]),
                ]
                g[4] = max(g[4], b[4])
            else:
                groups.append(b[:])
        if len(groups) != 1:
            return None, (
                "no_person_detected" if not groups else "multiple_people_unresolved"
            )
        return groups[0], "single_person_detected"

    def infer(self, im, b):
        cx = (b[0] + b[2]) / 2
        cy = (b[1] + b[3]) / 2
        w = (b[2] - b[0]) * 1.5
        h = (b[3] - b[1]) * 1.5
        w = max(w, h * 0.75)
        h = w / 0.75
        mat = np.array(
            [[192 / w, 0, 96 - cx * 192 / w], [0, 256 / h, 128 - cy * 256 / h]],
            np.float32,
        )
        roi = cv2.warpAffine(im, mat, (192, 256))
        roi = cv2.cvtColor(roi, cv2.COLOR_BGR2RGB).astype(np.float32)
        roi = (roi - np.array([123.675, 116.28, 103.53], np.float32)) / np.array(
            [58.395, 57.12, 57.375], np.float32
        )
        self.pose.setInput(roi.transpose(2, 0, 1)[None])
        sx, sy = self.pose.forward(["simcc_x", "simcc_y"])
        xs = sx[0].argmax(axis=1) / 2
        ys = sy[0].argmax(axis=1) / 2
        scores = np.minimum(sx[0].max(axis=1), sy[0].max(axis=1))
        return np.column_stack(
            (xs * w / 192 + cx - w / 2, ys * h / 256 + cy - h / 2, scores)
        )

    @staticmethod
    def agreement(primary, secondary, mapping, span):
        """Compare corresponding native points, independent of body orientation."""
        used, conflicts = [], []
        for a, b in mapping:
            if a not in primary or min(primary[a][2:]) < 0.5 or secondary[b, 2] < 0.5:
                continue
            used.append(a)
            if np.linalg.norm(np.asarray(primary[a][:2]) - secondary[b, :2]) > max(
                45, span * 0.5
            ):
                conflicts.append(a)
        return used, conflicts

    def arbitrate(self, im, s, fresh, planner, framing="upper_body", ratio=9 / 16):
        """Ground tracking before using fit/overflow. Never select by crop width."""
        h, w = im.shape[:2]
        box, status = self.person_box(im)
        e = {
            "status": status,
            "mode": "same_frame",
            "initial_status": s["status"],
            "person_model": "yolo11n-pose",
            "person_model_sha256": PERSON_SHA,
            "pose_model": "rtmpose-m-halpe26",
            "pose_model_sha256": RTM_SHA,
            "confidence_scope": "Independent localization scores are not MediaPipe visibility/presence probabilities.",
        }
        e["detector_views"] = getattr(self, "detector_views", ["native"])
        e["person_detection_view"] = getattr(self, "person_view", "native")
        result = dict(s)
        result["recovery"] = e
        if box is None or len(box) != 56:
            # Without independent identity, discard geometry; preserve it in diagnostics.
            e["original_pose"] = {k: v for k, v in s.items() if k != "recovery"}
            result = {
                k: v
                for k, v in s.items()
                if k in ("at_ms", "inference_ms", "extraction_attempts")
            }
            result.update(
                recovery=e, status="no_pose_detected", geometry_trust="unverified"
            )
            return result
        yolo = np.asarray(box[5:]).reshape(17, 3)
        span = max(
            90 * w / 1920,
            max(box[3] - box[1], box[2] - box[0]) * 0.15,
            float(np.linalg.norm(yolo[5, :2] - yolo[6, :2])),
        )
        mapping = [
            (0, 0),
            (2, 1),
            (5, 2),
            (7, 3),
            (8, 4),
            (11, 5),
            (12, 6),
            (13, 7),
            (14, 8),
            (15, 9),
            (16, 10),
        ]
        primary = {
            v["index"]: [v["x"], v["y"], v["visibility"], v["presence"]]
            for v in s.get("landmark_evidence", [])
        }
        used, conflicts = self.agreement(primary, yolo, mapping, span)
        e.update(
            person_bounds=box[:4],
            person_score=box[4],
            independent_landmarks=[
                {
                    "index": i,
                    "x": float(v[0]),
                    "y": float(v[1]),
                    "confidence": float(v[2]),
                }
                for i, v in enumerate(yolo)
            ],
            compared_primary_indices=used,
            conflicting_primary_indices=conflicts,
            agreement_tolerance_pixels=max(45, span * 0.5),
        )
        body_grounded = (
            all(i in used for i in (11, 12)) and len(set(used) & {0, 2, 5, 7, 8}) >= 2
        )
        suspicious = (
            bool(conflicts)
            or s["status"] != "fits_detected_upper_pose"
            or not body_grounded
            or not all(i in used for i in (15, 16))
        )
        if not suspicious:
            e["status"] = "primary_pose_grounded"
            result["geometry_trust"] = "independently_grounded"
            result["bounds_model"] = "mediapipe_full_tracked"
            return result
        e["original_pose"] = {k: v for k, v in s.items() if k != "recovery"}
        p = self.infer(im, box)
        e["landmarks"] = [
            {"index": i, "x": float(v[0]), "y": float(v[1]), "confidence": float(v[2])}
            for i, v in enumerate(p)
        ]
        # Two independent networks must support the same unique head and torso.
        rtm_ev = {i: v.tolist() for i, v in enumerate(p)}
        independent_used, independent_conflicts = self.agreement(
            rtm_ev, yolo, [(i, i) for i in range(11)], span
        )
        identity = (
            all(i in independent_used for i in (5, 6))
            and len(set(independent_used) & set(range(5))) >= 2
            and not any(i <= 6 for i in independent_conflicts)
        )
        e["independent_identity_verified"] = bool(identity)
        e["independent_conflicting_indices"] = independent_conflicts
        new = fresh()
        if new is not None:
            e["fresh_landmark_evidence"] = [
                {
                    "index": i,
                    "x": float(v[0]),
                    "y": float(v[1]),
                    "visibility": float(v[2]),
                    "presence": float(v[3]),
                }
                for i, v in enumerate(new[:23])
            ]
            new_ev = {i: v.tolist() for i, v in enumerate(new)}
            ny, cy = self.agreement(new_ev, yolo, mapping, span)
            nr, cr = self.agreement(new_ev, p, mapping, span)
            grounded = (
                identity
                and not cy
                and not cr
                and all(i in nr and i in ny for i in (11, 12))
                and len(set(nr) & {0, 2, 5, 7, 8}) >= 2
            )
            if grounded:
                result = {
                    k: v
                    for k, v in s.items()
                    if k in ("at_ms", "inference_ms", "extraction_attempts")
                }
                result.update(planner(new))
                result["landmark_evidence"] = e["fresh_landmark_evidence"]
                unsupported = [i for i in (15, 16) if i not in ny and i not in nr]
                result["low_confidence_wrist_indices"] = sorted(
                    set(result.get("low_confidence_wrist_indices", []) + unsupported)
                )
                if unsupported:
                    result["hand_support_status"] = "independent_hand_support_missing"
                    if result["status"] == "fits_detected_upper_pose":
                        result["status"] = "uncertain_hand_evidence"
                result.update(
                    recovery=e,
                    geometry_trust="independently_grounded",
                    bounds_model="mediapipe_full_fresh",
                )
                e["status"] = "whole_pose_reacquired_full"
                if result.get("low_confidence_wrist_indices"):
                    supported_pose = p.copy()
                    for i in (9, 10):
                        if (
                            i not in independent_used
                            or i in independent_conflicts
                            or i - 2 in independent_conflicts
                        ):
                            supported_pose[i, 2] = 0
                    repaired = self.recover(
                        im, result, framing, ratio, box, supported_pose
                    )
                    if repaired["recovery"]["status"] == "same_frame_hands_recovered":
                        e["hand_repair"] = repaired["recovery"]
                        repaired["recovery"] = e
                        result = repaired
                return result
        # Primary identity is rejected. Build an entirely new conservative pose,
        # without unioning the discarded skeleton's head or ghost limbs.
        if identity:
            face = p[[i for i in range(5) if p[i, 2] >= 0.5], :2]
            pad = max(28 * w / 1920, span * 0.22)
            head = [
                face[:, 0].min() - pad,
                face[:, 1].min() - pad,
                face[:, 0].max() + pad,
                face[:, 1].max() + pad,
            ]
            if p[17, 2] >= 0.5:
                head = [
                    min(head[0], p[17, 0] - pad),
                    min(head[1], p[17, 1] - pad),
                    max(head[2], p[17, 0] + pad),
                    max(head[3], p[17, 1] + pad),
                ]
            supported = [i for i in range(5, 11) if p[i, 2] >= 0.5]
            hand_pad = max(
                40 * w / 1920,
                max(
                    [
                        float(np.linalg.norm(p[a, :2] - p[b, :2])) * 0.20
                        for a, b in [(7, 9), (8, 10)]
                        if a in supported and b in supported
                    ],
                    default=0,
                ),
            )
            b = head[:]
            for i in supported:
                margin = hand_pad if i in (9, 10) else 28 * w / 1920
                b = [
                    min(b[0], p[i, 0] - margin),
                    min(b[1], p[i, 1] - margin),
                    max(b[2], p[i, 0] + margin),
                    max(b[3], p[i, 1] + margin),
                ]
            weak = [
                15 if i == 9 else 16
                for i in (9, 10)
                if i not in independent_used
                or p[i, 2] < 0.5
                or i in independent_conflicts
                or i - 2 in independent_conflicts
            ]
            raw = b[:]
            plan = box_plan(b, head, w, h, ratio, framing)
            result = {
                k: v
                for k, v in s.items()
                if k in ("at_ms", "inference_ms", "extraction_attempts")
            }
            result.update(
                plan,
                recovery=e,
                geometry_trust="independently_grounded",
                bounds_model="rtmpose_yolo_whole",
                head_margin_pixels=pad,
                hand_margin_pixels=hand_pad,
                landmark_span_width=float(
                    np.ptp(p[[i for i in range(11) if p[i, 2] >= 0.5], 0])
                ),
                low_confidence_wrist_indices=weak,
                required_upper_pose_width=plan["required_bounds"][2]
                - plan["required_bounds"][0],
                max_possible_crop_width=min(h * ratio, w),
                subject_extent_clipped_by_source=[
                    raw[0] < 0,
                    raw[1] < 0,
                    raw[2] > w,
                    raw[3] > h,
                ],
            )
            result["status"] = (
                "uncertain_hand_evidence"
                if weak
                else (
                    "fits_detected_upper_pose"
                    if plan["fits_bounds"]
                    else "upper_pose_exceeds_crop"
                )
            )
            e["status"] = "whole_pose_reacquired_independent"
            return result
        # A bounded independent person box guides position, never certifies pose.
        # Position-only fallback still retains independently supported head
        # geometry, including reclining/inverted subjects. A wide coarse body
        # box must never displace a verified head outside the crop.
        supported_head = None
        if len(set(used) & {0, 2, 5, 7, 8}) >= 2 and not any(
            i in {0, 2, 5, 7, 8} for i in conflicts
        ):
            supported_head = s.get("head_bounds_estimate")
        elif len(set(independent_used) & set(range(5))) >= 2 and not any(
            i < 5 for i in independent_conflicts
        ):
            hp = p[[i for i in range(5) if p[i, 2] >= 0.5], :2]
            hp_pad = max(40 * w / 1920, float(np.ptp(hp, axis=0).max()) * 0.60)
            supported_head = [
                hp[:, 0].min() - hp_pad,
                hp[:, 1].min() - hp_pad,
                hp[:, 0].max() + hp_pad,
                hp[:, 1].max() + hp_pad,
            ]
        e["position_head_grounded"] = bool(supported_head)
        plan = box_plan(box[:4], supported_head, w, h, ratio, framing)
        result = {
            k: v
            for k, v in s.items()
            if k in ("at_ms", "inference_ms", "extraction_attempts")
        }
        result.update(
            plan,
            recovery=e,
            status="uncertain_person_extent",
            extent_scope="full_person_recovery",
            geometry_trust="unverified",
            bounds_model="yolo_person_position_only",
        )
        e["status"] = "whole_pose_identity_unresolved"
        return result

    def recover(
        self, im, s, framing="upper_body", ratio=9 / 16, detected=None, inferred=None
    ):
        h, w = im.shape[:2]
        box, det_status = (
            (detected, "single_person_detected")
            if detected is not None
            else self.person_box(im)
        )
        e = {
            "status": det_status,
            "initial_status": s["status"],
            "mode": "same_frame",
            "person_model": "yolo11n-pose",
            "person_model_sha256": PERSON_SHA,
            "pose_model": "rtmpose-m-halpe26",
            "pose_model_sha256": RTM_SHA,
            "confidence_scope": "RTMPose localization scores; these are not MediaPipe visibility or presence probabilities.",
        }
        result = dict(s)
        result["recovery"] = e
        if box is None:
            return result
        p = inferred if inferred is not None else self.infer(im, box)
        e["person_bounds"] = box[:4]
        e["person_score"] = box[4]
        e["landmarks"] = [
            {"index": i, "x": float(v[0]), "y": float(v[1]), "confidence": float(v[2])}
            for i, v in enumerate(p)
        ]
        good = lambda i: p[i, 2] >= 0.5
        # A person box or a pose score alone cannot verify the subject's identity.
        face = [i for i in range(5) if good(i)]
        evidence = {v["index"]: v for v in s.get("landmark_evidence", [])}
        primary_good = (
            lambda i: i in evidence
            and min(evidence[i]["visibility"], evidence[i]["presence"]) >= 0.5
        )
        if s.get("required_bounds"):
            # Only recover weak hands when independent torso/head agree in native space.
            if not all(primary_good(i) and good(j) for i, j in [(11, 5), (12, 6)]):
                e["status"] = "torso_identity_unverified"
                return result
            span = max(
                50,
                np.linalg.norm(
                    np.array([evidence[11]["x"], evidence[11]["y"]])
                    - np.array([evidence[12]["x"], evidence[12]["y"]])
                ),
            )
            if any(
                np.linalg.norm(
                    np.array([evidence[i]["x"], evidence[i]["y"]]) - p[j, :2]
                )
                > span * 0.5
                for i, j in [(11, 5), (12, 6)]
            ):
                e["status"] = "torso_disagreement"
                return result
            pf = [i for i in range(11) if primary_good(i)]
            if (
                len(pf) < 3
                or len(face) < 2
                or np.linalg.norm(
                    np.mean([[evidence[i]["x"], evidence[i]["y"]] for i in pf], axis=0)
                    - np.mean(p[face, :2], axis=0)
                )
                > span * 0.5
            ):
                e["status"] = "head_disagreement"
                return result
            b = list(s["required_bounds"])
            accepted = []
            for i, j, elbow in [(15, 9, 7), (16, 10, 8)]:
                if primary_good(i) or not (good(j) and good(elbow)):
                    continue
                if not (0 <= p[j, 0] < w and 0 <= p[j, 1] < h):
                    continue
                fingers = [17, 19, 21] if i == 15 else [18, 20, 22]
                if any(
                    primary_good(k)
                    and np.linalg.norm(
                        np.array([evidence[k]["x"], evidence[k]["y"]]) - p[j, :2]
                    )
                    > span * 0.5
                    for k in fingers
                ):
                    continue
                pe = 13 if i == 15 else 14
                if (
                    primary_good(pe)
                    and np.linalg.norm(
                        np.array([evidence[pe]["x"], evidence[pe]["y"]]) - p[elbow, :2]
                    )
                    > span * 0.5
                ):
                    continue
                pad = max(
                    40 * w / 1920, float(np.linalg.norm(p[j, :2] - p[elbow, :2])) * 0.2
                )
                b = [
                    min(b[0], p[j, 0] - pad),
                    min(b[1], p[j, 1] - pad),
                    max(b[2], p[j, 0] + pad),
                    max(b[3], p[j, 1] + pad),
                ]
                accepted.append(i)
            if not accepted:
                e["status"] = "no_additional_hand_support"
                return result
            plan = box_plan(b, s.get("head_bounds_estimate"), w, h, ratio, framing)
            e["status"] = "same_frame_hands_recovered"
            e["recovered_wrist_indices"] = accepted
            weak = [
                i
                for i in s.get("low_confidence_wrist_indices", [])
                if i not in accepted
            ]
            result.update(plan)
            result["required_upper_pose_width"] = (
                plan["required_bounds"][2] - plan["required_bounds"][0]
            )
            result["hand_margin_pixels"] = max(result.get("hand_margin_pixels", 0), pad)
            result["low_confidence_wrist_indices"] = weak
            result["status"] = (
                "fits_detected_upper_pose"
                if plan["fits_bounds"] and not weak
                else (
                    "uncertain_hand_evidence"
                    if plan["fits_bounds"]
                    else "upper_pose_exceeds_crop"
                )
            )
            return result
        # Missing head/shoulder geometry: use the same frame's full person extent,
        # expanded by supported head/limb estimates. This stays composition-unknown.
        # A coarse person box may guide positioning even without verified head
        # geometry. It never establishes head/hand coverage.
        if len(face) < 2:
            pad = 28 * w / 1920
            b = [box[0] - pad, box[1] - pad, box[2] + pad, box[3] + pad]
            result.update(box_plan(b, None, w, h, ratio, framing))
            result["status"] = "uncertain_person_extent"
            result["extent_scope"] = "full_person_recovery"
            e["status"] = "same_frame_person_extent_head_unknown"
            return result
        supported = [
            i for i in list(range(13)) + [17, 18] if p[i, 2] >= 0.3
        ]  # weak points only enlarge, never establish coverage
        pad = 28 * w / 1920
        b = [box[0] - pad, box[1] - pad, box[2] + pad, box[3] + pad]
        for i in supported:
            b = [
                min(b[0], p[i, 0] - pad),
                min(b[1], p[i, 1] - pad),
                max(b[2], p[i, 0] + pad),
                max(b[3], p[i, 1] + pad),
            ]
        hp = p[face, :2]
        head = [
            hp[:, 0].min() - 40 * w / 1920,
            hp[:, 1].min() - 40 * w / 1920,
            hp[:, 0].max() + 40 * w / 1920,
            hp[:, 1].max() + 40 * w / 1920,
        ]
        if good(17):
            head = [
                min(head[0], p[17, 0] - pad),
                min(head[1], p[17, 1] - pad),
                max(head[2], p[17, 0] + pad),
                max(head[3], p[17, 1] + pad),
            ]
        b = [
            min(b[0], head[0]),
            min(b[1], head[1]),
            max(b[2], head[2]),
            max(b[3], head[3]),
        ]
        plan = box_plan(b, head, w, h, ratio, framing)
        result.update(plan)
        result["status"] = "uncertain_person_extent"
        result["extent_scope"] = "full_person_recovery"
        e["status"] = "same_frame_person_extent"
        return result
