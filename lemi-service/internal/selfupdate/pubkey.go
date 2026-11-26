package selfupdate

// releasePublicKeyB64 是 Lemi 发布签名的 Ed25519 公钥（32 字节，标准 base64）。
// 对应私钥在 GitHub Secrets LEMI_RELEASE_SIGNING_KEY 与维护者本机 ~/.lemi-secrets/release-signing.key，永不更换：
// 换了它，已经装好的旧版本就再也无法在线升级（只能手动下载新版本）。
const releasePublicKeyB64 = "Ky2GRU1PiUfbfJrwGpaDtzY3W976kZbs3GnRLjWIhQ8="
