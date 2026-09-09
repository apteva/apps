using UnityEngine;
namespace Apteva.Games {
public sealed class AptevaContentBundle : ScriptableObject { public string digest; public Sprite[] sprites; public AptevaContentClip[] clips; public AptevaContentData[] data; public Material[] materials; public Font[] fonts; public AudioClip[] audio; }
}
