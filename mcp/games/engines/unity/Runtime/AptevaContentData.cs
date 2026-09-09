using UnityEngine;
namespace Apteva.Games {
// Engine-neutral rig, stream, locale, table and style specifications. Gameplay
// code interprets these schemas; imported content never adds scenes or prefabs.
public sealed class AptevaContentData : ScriptableObject { public string kind; public string assetVersion; [TextArea] public string json; public byte[] bytes; }
}
