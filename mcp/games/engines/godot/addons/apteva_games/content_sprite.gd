class_name AptevaContentSprite
extends AnimatedSprite2D
# Assign an imported SpriteFrames resource. Pivots use the shared top-left
# convention; scale restores the original logical canvas for 2x/3x/4x bakes.
func _ready() -> void:
	texture_filter = CanvasItem.TEXTURE_FILTER_NEAREST
	centered = false
	frame_changed.connect(_apply_pivot)
	animation_changed.connect(_apply_pivot)
	_apply_pivot()
func _apply_pivot() -> void:
	if sprite_frames == null or not sprite_frames.has_animation(animation): return
	if frame >= sprite_frames.get_frame_count(animation): return
	var texture := sprite_frames.get_frame_texture(animation, frame)
	if texture == null: return
	var pivot: Vector2 = texture.get_meta("pivot", Vector2(0.5, 0.5))
	offset = -texture.get_size() * pivot
	var rendition_scale := float(sprite_frames.get_meta("content_scale", 1))
	scale = Vector2.ONE / rendition_scale
