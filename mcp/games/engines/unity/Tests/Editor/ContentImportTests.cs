using System;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using NUnit.Framework;
using UnityEditor;
using UnityEngine;
namespace Apteva.Games.Tests {
public sealed class ContentImportTests {
    string directory;
    static string Hash(byte[] bytes){using(var sha=SHA256.Create())return BitConverter.ToString(sha.ComputeHash(bytes)).Replace("-", "").ToLowerInvariant();}
    [SetUp] public void SetUp(){directory="Assets/AptevaContentTest-"+Guid.NewGuid().ToString("N");Directory.CreateDirectory(directory);}
    [TearDown] public void TearDown(){AssetDatabase.DeleteAsset(directory);if(Directory.Exists(directory))Directory.Delete(directory,true);}
    [Test] public void ImportsFramesWithStableIDsPivotsAndTiming(){
        var texture=new Texture2D(8,4,TextureFormat.RGBA32,false);texture.SetPixels(Enumerable.Repeat(Color.red,32).ToArray());texture.Apply();var image=texture.EncodeToPNG();UnityEngine.Object.DestroyImmediate(texture);
        var path="hero/"+Hash(image)+".png";Directory.CreateDirectory(directory+"/hero");File.WriteAllBytes(directory+"/"+path,image);
        var frames=new[]{new ContentFrame{name="one",x=0,y=0,width=4,height=4,pivot_x=.5f,pivot_y=1,duration_ms=100},new ContentFrame{name="two",x=4,y=0,width=4,height=4,pivot_x=.5f,pivot_y=1,duration_ms=200}};
        var manifest=new ContentManifest{schema="apteva.games.content/v1",assets=new[]{new ContentRendition{asset="hero",kind="spriteset",version="v1",target="unity",engine_version=Application.unityVersion,platform="desktop",pixels_per_unit=4,scale=1,files=new[]{new ContentFile{path=path,sha256=Hash(image),mime="image/png",size=image.Length}},frames=frames,animations=new[]{new ContentAnimation{name="idle",frames=new[]{"one","two"},loop=true}}}}};
        var bytes=Encoding.UTF8.GetBytes(JsonUtility.ToJson(manifest));File.WriteAllText(directory+"/content.lock.json",JsonUtility.ToJson(new ContentLock{digest=Hash(bytes)}));var bundlePath=directory+"/bundle.gamescontent";File.WriteAllBytes(bundlePath,bytes);AssetDatabase.Refresh(ImportAssetOptions.ForceSynchronousImport);AssetDatabase.ImportAsset(bundlePath,ImportAssetOptions.ForceSynchronousImport);
        var bundle=AssetDatabase.LoadAssetAtPath<AptevaContentBundle>(bundlePath);Assert.That(bundle,Is.Not.Null);Assert.That(bundle.sprites.Length,Is.EqualTo(2));Assert.That(bundle.sprites[0].pivot,Is.EqualTo(new Vector2(2,0)));Assert.That(bundle.clips[0].durationMs,Is.EqualTo(new[]{100,200}));
        string guid;long before;Assert.That(AssetDatabase.TryGetGUIDAndLocalFileIdentifier(bundle.sprites[0],out guid,out before),Is.True);
        AssetDatabase.ImportAsset(bundlePath,ImportAssetOptions.ForceUpdate|ImportAssetOptions.ForceSynchronousImport);bundle=AssetDatabase.LoadAssetAtPath<AptevaContentBundle>(bundlePath);long after;Assert.That(AssetDatabase.TryGetGUIDAndLocalFileIdentifier(bundle.sprites[0],out guid,out after),Is.True);Assert.That(after,Is.EqualTo(before));
    }
}
}
