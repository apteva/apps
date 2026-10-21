export type Camera = { x: number; y: number; zoom: number };
export const FIT_CAMERA: Camera = { x: .5, y: .5, zoom: 1 };
export function clampCamera(camera: Camera): Camera {
 const zoom = Math.max(1, Math.min(8, camera.zoom));
 const edge = .5 / zoom;
 return {zoom, x:Math.max(edge,Math.min(1-edge,camera.x)), y:Math.max(edge,Math.min(1-edge,camera.y))};
}
// Coordinates are normalized to the full scene, so every renderer shares a
// camera independent of its SVG dimensions or responsive aspect ratio.
export function zoomCamera(camera: Camera, factor: number, anchor = {x:.5,y:.5}): Camera {
 const zoom=Math.max(1,Math.min(8,camera.zoom*factor));
 return clampCamera({zoom,x:camera.x+(anchor.x-.5)*(1/camera.zoom-1/zoom),y:camera.y+(anchor.y-.5)*(1/camera.zoom-1/zoom)});
}
export function panCamera(camera: Camera, dx: number, dy: number): Camera {
 return clampCamera({...camera,x:camera.x-dx/camera.zoom,y:camera.y-dy/camera.zoom});
}
export function cameraBox(camera: Camera, width: number, height: number) {
 return `${(camera.x-.5/camera.zoom)*width} ${(camera.y-.5/camera.zoom)*height} ${width/camera.zoom} ${height/camera.zoom}`;
}
