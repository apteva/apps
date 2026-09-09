// Unity 6 importer. Put this file in Assets/Editor and the runtime class in
// Assets/Scripts. Copy manifest.json to bundle.gamescontent beside the lockfile.
using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using UnityEditor;
using UnityEditor.AssetImporters;
using UnityEngine;

namespace Apteva.Games {
[Serializable] public class ContentMaterial { public string model,texture; public float[] tint; }
[Serializable] public class ContentLock { public string digest; }
[Serializable] public class ContentFile { public string path, sha256, mime; public int size; }
[Serializable] public class ContentFrame { public string name; public int x,y,width,height,duration_ms; public float pivot_x,pivot_y; }
[Serializable] public class ContentAnimation { public string name; public string[] frames; public bool loop; }
[Serializable] public class ContentRendition { public string id,asset,kind,version,target,engine_version,platform; public float pixels_per_unit; public int scale; public ContentFile[] files; public ContentFrame[] frames; public ContentAnimation[] animations; }
[Serializable] public class ContentManifest { public string schema; public ContentRendition[] assets; }

[ScriptedImporter(1, "gamescontent")]
public sealed class AptevaContentImporter : ScriptedImporter {
    public string platform = "desktop";
    static string Hash(byte[] b) { using(var h=SHA256.Create()) return BitConverter.ToString(h.ComputeHash(b)).Replace("-", "").ToLowerInvariant(); }
    public override void OnImportAsset(AssetImportContext ctx) {
        var directory=Path.GetDirectoryName(ctx.assetPath);
        var lockPath=Path.Combine(directory,"content.lock.json");
        ctx.DependsOnSourceAsset(lockPath);
        var locked=JsonUtility.FromJson<ContentLock>(File.ReadAllText(lockPath));
        var bytes=File.ReadAllBytes(ctx.assetPath);
        if(locked==null || Hash(bytes)!=locked.digest) throw new InvalidDataException("Content manifest differs from its lockfile");
        var manifest=JsonUtility.FromJson<ContentManifest>(Encoding.UTF8.GetString(bytes));
        if(manifest==null || manifest.schema!="apteva.games.content/v1") throw new InvalidDataException("Unsupported content schema");
        var renditions=manifest.assets.Where(r=>r.target=="unity" && r.platform==platform).ToArray();
        if(renditions.Length==0) throw new InvalidDataException("No Unity renditions for this platform");
        foreach(var r in renditions) {
            if(!(Application.unityVersion==r.engine_version || Application.unityVersion.StartsWith(r.engine_version+".",StringComparison.Ordinal))) throw new InvalidDataException("Unity version does not match pinned rendition");
            foreach(var file in r.files) {
                if(Path.IsPathRooted(file.path) || file.path.Contains("..") || file.path.Contains("\\") || file.path.Contains(":")) throw new InvalidDataException("Unsafe content path");
                var path=Path.Combine(directory,file.path); var data=File.ReadAllBytes(path);
                ctx.DependsOnSourceAsset(path);
                if(data.Length!=file.size || Hash(data)!=file.sha256) throw new InvalidDataException("Content checksum mismatch: "+file.path);
            }
        }
        var bundle=ScriptableObject.CreateInstance<AptevaContentBundle>(); bundle.digest=locked.digest;
        var animations=new List<AptevaContentClip>();
        var sprites=new List<Sprite>();
        var texturesByAsset=new Dictionary<string,Texture2D>();
        var dataAssets=new List<AptevaContentData>();var materials=new List<Material>();var fonts=new List<Font>();var audio=new List<AudioClip>();
        foreach(var r in renditions.OrderBy(r=>r.kind=="material"?1:0)) {
            if(r.kind!="sprite" && r.kind!="spriteset" && r.kind!="tileset") {
                var source=Path.Combine(directory,r.files[0].path).Replace('\\','/');
                if(r.kind=="font" || r.kind=="sfx" || r.kind=="music") {
                    ctx.DependsOnArtifact(source);
                    if(r.kind=="font") {var font=AssetDatabase.LoadAssetAtPath<Font>(source);if(font==null)throw new InvalidDataException("Import font source first");fonts.Add(font);}
                    else {var clip=AssetDatabase.LoadAssetAtPath<AudioClip>(source);if(clip==null)throw new InvalidDataException("Import audio source first");audio.Add(clip);}
                } else if(r.kind=="material") {
                    var spec=JsonUtility.FromJson<ContentMaterial>(File.ReadAllText(source));var shader=Shader.Find("Sprites/Default");if(shader==null)throw new InvalidDataException("Sprites/Default shader unavailable");
                    var material=new Material(shader);material.name=r.asset;material.color=new Color(spec.tint[0],spec.tint[1],spec.tint[2],spec.tint[3]);if(!string.IsNullOrEmpty(spec.texture)){Texture2D sourceTexture;if(!texturesByAsset.TryGetValue(spec.texture,out sourceTexture))throw new InvalidDataException("Material texture rendition missing");material.mainTexture=sourceTexture;}ctx.AddObjectToAsset(r.asset+"/material",material);materials.Add(material);
                } else {
                    var data=ScriptableObject.CreateInstance<AptevaContentData>();data.name=r.asset;data.kind=r.kind;data.assetVersion=r.version;
                    if(r.kind=="blob")data.bytes=File.ReadAllBytes(source);else data.json=File.ReadAllText(source);
                    ctx.AddObjectToAsset(r.asset+"/data",data);dataAssets.Add(data);
                }
                continue;
            }
            var texture=new Texture2D(2,2,TextureFormat.RGBA32,false);
            if(!ImageConversion.LoadImage(texture,File.ReadAllBytes(Path.Combine(directory,r.files[0].path)),false)) throw new InvalidDataException("Invalid PNG");
            texture.name=r.asset+" atlas"; texture.filterMode=FilterMode.Point; texture.wrapMode=TextureWrapMode.Clamp;
            ctx.AddObjectToAsset(r.asset+"/atlas",texture);texturesByAsset.Add(r.asset,texture);
            var frameMap=new Dictionary<string,Sprite>();
            foreach(var f in r.frames) {
                var sprite=Sprite.Create(texture,new Rect(f.x,texture.height-f.y-f.height,f.width,f.height),new Vector2(f.pivot_x,1-f.pivot_y),r.pixels_per_unit,0,SpriteMeshType.FullRect);
                sprite.name=r.asset+"/"+f.name;
                // Identifiers depend on logical names, not atlas positions or hashes.
                ctx.AddObjectToAsset(sprite.name,sprite); frameMap.Add(f.name,sprite); sprites.Add(sprite);
            }
            foreach(var animation in r.animations??Array.Empty<ContentAnimation>()) {
                var clip=ScriptableObject.CreateInstance<AptevaContentClip>();clip.name=r.asset+"/"+animation.name;clip.loop=animation.loop;
                clip.frames=animation.frames.Select(n=>frameMap[n]).ToArray();
                clip.durationMs=animation.frames.Select(n=>r.frames.First(f=>f.name==n).duration_ms).ToArray();
                ctx.AddObjectToAsset(clip.name,clip);animations.Add(clip);
            }
        }
        bundle.data=dataAssets.ToArray();bundle.materials=materials.ToArray();bundle.fonts=fonts.ToArray();bundle.audio=audio.ToArray();bundle.sprites=sprites.ToArray();bundle.clips=animations.ToArray();ctx.AddObjectToAsset("bundle",bundle);ctx.SetMainObject(bundle);
    }
}
}
