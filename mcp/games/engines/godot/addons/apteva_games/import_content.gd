extends SceneTree
# Run after --editor --import. Imports a verified lockfile, never a live head.
var content_dir := "res://GameContent"
var platform := "desktop"
var failure := ""
func _initialize() -> void:
	for arg in OS.get_cmdline_user_args():
		if arg.begins_with("--content="): content_dir = arg.trim_prefix("--content=").trim_suffix("/")
		if arg.begins_with("--platform="): platform = arg.trim_prefix("--platform=")
	if not content_dir.begins_with("res://") or ".." in content_dir:
		fail("Content must be inside the project"); return
	var lock_text := FileAccess.get_file_as_string(content_dir + "/content.lock.json")
	var lock = JSON.parse_string(lock_text)
	var manifest_text := FileAccess.get_file_as_string(content_dir + "/manifest.json")
	if not lock is Dictionary or manifest_text.sha256_text() != lock.get("digest", ""):
		fail("Manifest checksum does not match content.lock.json"); return
	var manifest = JSON.parse_string(manifest_text)
	if not manifest is Dictionary or manifest.get("schema") != "apteva.games.content/v1":
		fail("Unsupported content schema"); return
	var selected: Array = []
	for rendition in manifest.get("assets", []):
		if rendition.get("target") != "godot" or rendition.get("platform") != platform: continue
		if rendition.get("engine_version") != "%d.%d.%d" % [Engine.get_version_info().major, Engine.get_version_info().minor, Engine.get_version_info().patch]:
			fail("Engine version does not match pinned rendition: " + str(rendition.get("engine_version"))); return
		for file in rendition.files:
			var relative: String = file.path
			if relative.begins_with("/") or ".." in relative or "\\" in relative or ":" in relative:
				fail("Unsafe content path"); return
			var path := content_dir + "/" + relative
			if FileAccess.get_sha256(path) != file.sha256 or FileAccess.get_file_as_bytes(path).size() != int(file.size):
				fail("Content checksum mismatch: " + relative); return
		selected.append(rendition)
	if selected.is_empty(): fail("No Godot renditions for this platform"); return
	# Verify every input before writing any generated resource. Outputs are scoped
	# by digest, so a failed import cannot replace an earlier working release.
	var output := content_dir + "/imports/" + str(lock.digest)
	DirAccess.make_dir_recursive_absolute(output)
	var imported: Array = []
	for rendition in selected:
		var kind: String = rendition.kind
		if kind not in ["sprite", "spriteset", "tileset"]:
			var source_path: String = content_dir + "/" + rendition.files[0].path
			var resource: Resource
			if kind in ["font", "sfx", "music"]:
				resource = load(source_path)
				if resource == null: fail("Run --editor --import before loading " + kind); return
			elif kind == "blob":
				resource = Resource.new()
				resource.set_meta("bytes", FileAccess.get_file_as_bytes(source_path))
			else:
				var data = JSON.parse_string(FileAccess.get_file_as_string(source_path))
				if not data is Dictionary: fail("Invalid structured asset"); return
				if kind == "material":
					var material := ShaderMaterial.new()
					var shader := Shader.new()
					shader.code = "shader_type canvas_item; uniform vec4 tint : source_color = vec4(1.0); void fragment() { COLOR *= tint; }"
					if data.get("texture", "") != "":
						shader.code = "shader_type canvas_item; uniform vec4 tint : source_color = vec4(1.0); uniform sampler2D asset_texture : source_color, filter_nearest; void fragment() { COLOR = texture(asset_texture, UV) * tint; }"
						var texture_path := ""
						for dependency in selected:
							if dependency.asset == data.texture: texture_path = content_dir + "/" + dependency.files[0].path
						if texture_path == "": fail("Material texture rendition missing"); return
						material.shader = shader
						material.set_shader_parameter("asset_texture", load(texture_path))
					material.shader = shader
					material.set_shader_parameter("tint", Color(data.tint[0],data.tint[1],data.tint[2],data.tint[3]))
					resource = material
				else: resource = Resource.new()
				resource.set_meta("data", data)
			resource.set_meta("asset_version", rendition.version)
			resource.set_meta("manifest_digest", lock.digest)
			var result_path: String = output + "/" + rendition.asset + ".tres"
			if ResourceSaver.save(resource,result_path) != OK: fail("Could not save " + kind); return
			imported.append(result_path)
			continue
		var texture = load(content_dir + "/" + rendition.files[0].path)
		if not texture is Texture2D: fail("Run Godot --editor --import before importing content"); return
		var frames := SpriteFrames.new()
		frames.remove_animation("default")
		var textures := {}
		for f in rendition.frames:
			var atlas := AtlasTexture.new()
			atlas.atlas = texture
			atlas.region = Rect2(f.x, f.y, f.width, f.height)
			atlas.filter_clip = true
			atlas.set_meta("pivot", Vector2(f.pivot_x, f.pivot_y))
			textures[f.name] = atlas
		var animations: Array = rendition.get("animations", [])
		if animations.is_empty():
			var names: Array = []
			for f in rendition.frames: names.append(f.name)
			animations = [{"name":"default", "frames":names, "loop":false}]
		for animation in animations:
			frames.add_animation(animation.name)
			frames.set_animation_speed(animation.name, 1000.0)
			frames.set_animation_loop(animation.name, animation.loop)
			for name in animation.frames:
				var duration := 100.0
				for f in rendition.frames:
					if f.name == name: duration = float(f.duration_ms)
				frames.add_frame(animation.name, textures[name], duration)
		frames.set_meta("manifest_digest", lock.digest)
		frames.set_meta("asset_version", rendition.version)
		frames.set_meta("content_scale", rendition.scale)
		var resource_path: String = output + "/" + rendition.asset + ".tres"
		if ResourceSaver.save(frames, resource_path) != OK: fail("Could not save SpriteFrames"); return
		imported.append(resource_path)
		if kind == "tileset":
			var first = rendition.frames[0]
			var tile_size := Vector2i(first.width, first.height)
			var source := TileSetAtlasSource.new()
			source.texture = texture
			source.texture_region_size = tile_size
			source.margins = Vector2i(2,2)
			source.separation = Vector2i(4,4)
			for f in rendition.frames:
				if Vector2i(f.width,f.height) != tile_size: fail("Tile frames must have equal dimensions"); return
				source.create_tile(Vector2i(int(f.x-2)/int(f.width+4),int(f.y-2)/int(f.height+4)))
			var tiles := TileSet.new()
			tiles.tile_size = tile_size
			tiles.add_source(source)
			if ResourceSaver.save(tiles,output+"/"+rendition.asset+"-tiles.tres") != OK: fail("Could not save TileSet"); return
	var receipt := FileAccess.open(output+"/import-receipt.json",FileAccess.WRITE)
	if receipt == null: fail("Could not save import receipt"); return
	receipt.store_string(JSON.stringify({"digest":lock.digest,"engine":Engine.get_version_info().string,"platform":platform,"resources":imported}))
	receipt.close()
	print("Imported Games content ", lock.digest, " (", imported.size(), " resources)")
	quit(0)
func fail(message: String) -> void:
	push_error(message)
	quit(1)
