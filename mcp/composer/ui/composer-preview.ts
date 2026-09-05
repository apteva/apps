export interface PreviewCrop { x: number; y: number; width: number; height: number }
export interface PreviewKeyframe { time: number; x?: number; y?: number; scale?: number; easing?: "linear" | "ease_in" | "ease_out" | "ease_in_out" }
export interface PreviewTransform { x?: number; y?: number; scale?: number; keyframes?: PreviewKeyframe[] }

export function previewEase(progress: number, easing?: PreviewKeyframe["easing"]): number {
  const p = Math.max(0, Math.min(1, progress));
  if (easing === "ease_in") return p * p;
  if (easing === "ease_out") return 1 - (1 - p) * (1 - p);
  if (easing === "ease_in_out") return p < 0.5 ? 2 * p * p : 1 - Math.pow(-2 * p + 2, 2) / 2;
  return p;
}

export function previewCameraAt(transform: PreviewTransform | undefined, time: number): { x: number; y: number; scale: number } {
  let current = { time: 0, x: transform?.x ?? 0.5, y: transform?.y ?? 0.5, scale: transform?.scale || 1, easing: "linear" as PreviewKeyframe["easing"] };
  const points = [current];
  for (const keyframe of transform?.keyframes || []) {
    current = {
      time: keyframe.time,
      x: keyframe.x ?? current.x,
      y: keyframe.y ?? current.y,
      scale: keyframe.scale || current.scale,
      easing: keyframe.easing || "linear",
    };
    if (current.time === 0) points[0] = current;
    else points.push(current);
  }
  if (points.length === 1 || time <= points[0].time) return points[0];
  for (let index = 1; index < points.length; index++) {
    const next = points[index];
    const previous = points[index - 1];
    if (time > next.time) continue;
    const progress = previewEase((time - previous.time) / Math.max(0.001, next.time - previous.time), next.easing);
    return {
      x: previous.x + (next.x - previous.x) * progress,
      y: previous.y + (next.y - previous.y) * progress,
      scale: previous.scale + (next.scale - previous.scale) * progress,
    };
  }
  return points[points.length - 1];
}

export function previewCropStyle(crop: PreviewCrop | undefined, fit: string, camera: { x: number; y: number; scale: number }): Record<string, string | number> {
  return {
    position: "absolute",
    width: crop ? `${100 / crop.width}%` : "100%",
    height: crop ? `${100 / crop.height}%` : "100%",
    left: crop ? `${-100 * crop.x / crop.width}%` : 0,
    top: crop ? `${-100 * crop.y / crop.height}%` : 0,
    objectFit: fit === "stretch" ? "fill" : fit === "contain" || fit === "none" ? "contain" : "cover",
    transform: `scale(${camera.scale})`,
    transformOrigin: `${camera.x * 100}% ${camera.y * 100}%`,
  };
}
