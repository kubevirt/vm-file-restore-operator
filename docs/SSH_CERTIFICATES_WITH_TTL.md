# SSH Certificates with TTL - Complexity Analysis

## Context

**Current situation:** The vm-file-restore-operator uses a single global ED25519 SSH keypair stored in a Kubernetes Secret. The same private key is used to SSH to ALL VMs, creating a significant security risk:

- **Single point of compromise:** If the Secret is compromised, attacker has SSH access to entire VM fleet
- **No expiration:** SSH keys don't expire like TLS certificates
- **No automatic cleanup:** Keys stay in VM authorized_keys forever after restore completes
- **No isolation:** All VMs trust the same public key

**Proposed solution:** SSH Certificates with TTL (Time-To-Live) using OpenSSH certificate authentication. Instead of distributing the same public key to every VM, we:

1. Create a CA (Certificate Authority) keypair once
2. VMs trust the CA public key (one-time setup)
3. For each restore, generate ephemeral certificate signed by CA (valid 1 hour)
4. Certificate expires automatically - no cleanup needed

## Complexity Assessment: MODERATE (5-6 / 10)

**Time estimate:** 2-3 days (implementation + testing + documentation)

| Aspect | Complexity | Reason |
|--------|-----------|--------|
| Conceptual | Medium | SSH certificates are less common than TLS certs, requires understanding principals/extensions |
| Operator Changes | Low-Medium | ~200 lines across 3 files (keypair.go, ssh.go, phases.go) |
| Guest Changes | Low | Setup scripts change public key → CA key, update sshd_config |
| Testing | Medium | Need to verify cert generation, expiration, principal validation |
| Deployment | Low | No migration needed - redeploy VMs with new setup scripts |

## How SSH Certificates Work

### Traditional SSH Key Auth (Current)
```
Operator                          Guest VM
┌──────────────┐                 ┌─────────────────────────┐
│ Private Key  │──SSH login───→  │ authorized_keys:        │
│ (Secret)     │                 │   ssh-ed25519 AAAA...   │
└──────────────┘                 │ (trusts THIS key only)  │
                                 └─────────────────────────┘
```

### SSH Certificate Auth (Proposed)
```
Operator                                    Guest VM
┌────────────────────────────┐             ┌─────────────────────────┐
│ CA Private Key (Secret)    │             │ TrustedUserCAKeys:      │
│      ↓                     │             │   ssh-ed25519 AAAA...   │
│ Generate cert for restore: │             │ (trusts CA signature)   │
│   - Valid 1 hour           │──SSH───→    │                         │
│   - Principal: filerestore │   login     │ Validates:              │
│   - Signed by CA           │             │   1. CA signature ✓     │
└────────────────────────────┘             │   2. Not expired ✓      │
                                           │   3. Principal match ✓  │
                                           └─────────────────────────┘
```

**Key differences:**
- VMs trust the **CA public key**, not individual user keys
- Operator signs **short-lived certificates** (1 hour) with CA private key
- Certificates contain **principals** (username), **validity period**, and **extensions**
- SSH automatically validates signature, expiration, and principal match
- No cleanup needed - certs expire on their own

## Required Changes

### 1. Operator: CA Generation (keypair.go)

**Current:**
```go
// EnsureSSHKeypair generates ED25519 keypair
func EnsureSSHKeypair(ctx context.Context, c client.Client) error {
    // Generate user keypair
    pub, priv, _ := ed25519.GenerateKey(rand.Reader)
    
    // Store in Secret (ssh-privatekey) + ConfigMap (ssh-publickey)
}
```

**New:**
```go
// EnsureSSHCA generates CA keypair for signing certificates
func EnsureSSHCA(ctx context.Context, c client.Client) error {
    secret := &corev1.Secret{}
    
    // Check if CA already exists
    err := c.Get(ctx, client.ObjectKey{
        Name: "vm-file-restore-ca",
        Namespace: operatorNamespace,
    }, secret)
    
    if err == nil {
        return nil // CA exists
    }
    
    // Generate CA keypair
    caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
    
    // Create SSH public key for CA
    sshPubKey, _ := ssh.NewPublicKey(caPub)
    
    // Store CA private key in Secret
    secret = &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name: "vm-file-restore-ca",
            Namespace: operatorNamespace,
        },
        Type: corev1.SecretTypeSSHAuth,
        Data: map[string][]byte{
            "ssh-privatekey": ssh.MarshalPrivateKey(crypto.PrivateKey(caPriv)),
            "ssh-publickey":  ssh.MarshalAuthorizedKey(sshPubKey), // For guests
        },
    }
    
    return c.Create(ctx, secret)
}
```

**Changes:**
- Rename Secret from `vm-file-restore-operator-ssh` → `vm-file-restore-ca`
- Still ED25519, still stored same way
- ConfigMap no longer needed (CA public key in Secret is enough)
- ~30 lines changed

### 2. Operator: Certificate Generation (ssh.go)

**New function:**
```go
// GenerateSSHCertificate creates short-lived cert signed by CA
func GenerateSSHCertificate(
    caPrivateKey []byte,
    principal string,
    validDuration time.Duration,
) ([]byte, error) {
    // Parse CA private key
    caSigner, _ := ssh.ParsePrivateKey(caPrivateKey)
    
    // Generate ephemeral user keypair for this restore
    userPub, userPriv, _ := ed25519.GenerateKey(rand.Reader)
    sshUserPub, _ := ssh.NewPublicKey(userPub)
    
    // Create certificate
    cert := &ssh.Certificate{
        Key:             sshUserPub,
        CertType:        ssh.UserCert,
        KeyId:           fmt.Sprintf("vm-restore-%d", time.Now().Unix()),
        ValidPrincipals: []string{principal}, // "filerestore"
        ValidAfter:      uint64(time.Now().Add(-1 * time.Minute).Unix()), // 1 min grace
        ValidBefore:     uint64(time.Now().Add(validDuration).Unix()),   // 1 hour
        Permissions: ssh.Permissions{
            Extensions: map[string]string{
                "permit-pty": "", // Allow PTY if needed for troubleshooting
            },
        },
    }
    
    // Sign certificate with CA private key
    _ = cert.SignCert(rand.Reader, caSigner)
    
    // Marshal certificate
    certBytes := ssh.MarshalAuthorizedKey(cert)
    
    // Marshal user private key
    userPrivBytes := ssh.MarshalPrivateKey(crypto.PrivateKey(userPriv))
    
    return userPrivBytes, certBytes, nil
}
```

**Usage in ConnectSSH:**
```go
func ConnectSSH(ip string, caPrivateKey []byte) (*SSHClient, error) {
    // Generate ephemeral certificate (1 hour validity)
    userPrivKey, certBytes, _ := GenerateSSHCertificate(
        caPrivateKey,
        "filerestore",
        1 * time.Hour,
    )
    
    // Parse user private key
    signer, _ := ssh.ParsePrivateKey(userPrivKey)
    
    // Parse certificate
    pubKey, _, _, _, _ := ssh.ParseAuthorizedKey(certBytes)
    cert := pubKey.(*ssh.Certificate)
    
    // Create cert signer
    certSigner, _ := ssh.NewCertSigner(cert, signer)
    
    // SSH config with certificate
    config := &ssh.ClientConfig{
        User: "filerestore",
        Auth: []ssh.AuthMethod{
            ssh.PublicKeys(certSigner), // Use certificate instead of raw key
        },
        HostKeyCallback: ssh.InsecureIgnoreHostKey(),
        Timeout:         10 * time.Second,
    }
    
    // Connect...
}
```

**Changes:**
- Add `GenerateSSHCertificate()` function (~50 lines)
- Modify `ConnectSSH()` to generate cert before connecting (~20 lines changed)
- Total: ~70 lines

### 3. Operator: Phase Handlers (phases.go)

**Change in handleSSHConnectingPhase and handleRestoringPhase:**

```go
// Get CA private key from Secret instead of user private key
secret := &corev1.Secret{}
err := r.Get(ctx, client.ObjectKey{
    Name:      "vm-file-restore-ca",  // Changed from vm-file-restore-operator-ssh
    Namespace: operatorNamespace,
}, secret)

caPrivateKey := secret.Data["ssh-privatekey"]

// Connect with cert generation
sshClient, err := ConnectSSH(ip, caPrivateKey)
```

**Changes:**
- Secret name change in 3 places
- Total: ~10 lines changed

### 4. Guest Setup Scripts

**Linux (setup.sh):**

**Current:**
```bash
# Add public key to authorized_keys
echo "$PUB_KEY" >> ~filerestore/.ssh/authorized_keys
```

**New:**
```bash
# Add CA public key to trusted CA keys
echo "$CA_PUB_KEY" > ~filerestore/.ssh/ca-keys.pub
chmod 600 ~filerestore/.ssh/ca-keys.pub
chown filerestore:filerestore ~filerestore/.ssh/ca-keys.pub

# Configure sshd to trust CA
if ! grep -q "^TrustedUserCAKeys" /etc/ssh/sshd_config; then
    echo "TrustedUserCAKeys /home/filerestore/.ssh/ca-keys.pub" >> /etc/ssh/sshd_config
    systemctl restart sshd
fi
```

**Windows (setup.bat):**

**Current:**
```powershell
Set-Content -Path "C:\ProgramData\ssh\administrators_authorized_keys" -Value $PubKey
```

**New:**
```powershell
# Write CA public key
Set-Content -Path "C:\ProgramData\ssh\ca-keys.pub" -Value $CAPubKey -Encoding ASCII

# Update sshd_config
$config = Get-Content "C:\ProgramData\ssh\sshd_config"
if ($config -notmatch "^TrustedUserCAKeys") {
    Add-Content "C:\ProgramData\ssh\sshd_config" "TrustedUserCAKeys C:/ProgramData/ssh/ca-keys.pub"
    Restart-Service sshd
}
```

**Changes:**
- Setup scripts: ~20 lines changed per script
- README documentation: ~30 lines

## Migration Path

**No automatic migration needed:**

1. Deploy updated operator (with CA generation)
2. Redeploy/recreate VMs with new setup scripts
3. Old VMs with authorized_keys continue to work (different auth method)
4. New VMs use CA-based auth
5. Gradually migrate VMs as they're rebuilt

**For forced migration:**
- Run new setup script on existing VMs (adds CA trust)
- Old authorized_keys can be removed after CA is trusted

## Security Benefits

### Before (Current):
- Global key compromise = access to ALL VMs
- Keys never expire
- Keys stay in VMs forever
- No per-VM isolation

### After (SSH Certificates):
- CA key compromise still risky, but certificates expire in 1 hour
- Automatic expiration - no cleanup needed
- Per-restore ephemeral certificates
- Can revoke CA and rotate (all certs become invalid)
- Can add custom extensions/restrictions per certificate

**Blast radius:** Still have CA private key risk, but time-limited (1-hour window vs forever)

## Alternative: Per-VM Ephemeral Keys

**Simpler approach without certificates:**

```go
// Generate unique keypair per VMFR
func (r *Reconciler) handleInitPhase(vmfr *VMFR) {
    // Generate ephemeral keypair for THIS restore only
    pub, priv, _ := ed25519.GenerateKey()
    
    // Store in VMFR-specific Secret
    secret := &Secret{
        Name: vmfr.Name + "-ssh-key",
        Data: map[string][]byte{
            "ssh-privatekey": priv,
            "ssh-publickey":  pub,
        },
    }
    c.Create(ctx, secret)
    
    // TODO: Inject public key to VM via cloud-init or agent
}
```

**Pros:**
- Simpler than certificates (~100 lines total)
- Per-VM isolation (compromise of one key doesn't affect others)
- Automatic cleanup when VMFR is deleted

**Cons:**
- Requires key injection mechanism (cloud-init, qemu guest agent, or API)
- No automatic expiration (cleanup depends on CR deletion)
- More Secrets to manage (one per restore)

## Recommendation

### For This Operator:

**Option A: Per-VM Ephemeral Keys** (Complexity: 3-4/10, Time: 1-2 days)
- Simpler implementation
- Better isolation than global key
- Requires key injection mechanism (blocker if not available)

**Option B: SSH Certificates with TTL** (Complexity: 5-6/10, Time: 2-3 days)
- Clean automatic expiration
- No key injection needed (one-time CA setup)
- Industry best practice for large-scale SSH

**Option C: Keep Global Key + Periodic Rotation** (Complexity: 2-3/10, Time: 1 day)
- Rotate global keypair every 90 days
- Roll new key to all VMs with grace period
- Minimal code changes
- Still a single point of failure

### Recommended: Option B (SSH Certificates)

**Why:**
1. No key injection dependency - works with existing setup scripts
2. Automatic expiration - no cleanup code needed
3. Industry standard - used by large organizations (Google, Facebook, etc.)
4. Future-proof - can add more restrictions/extensions later
5. Moderate complexity - ~200 lines of Go code, well-documented pattern

**Implementation order:**
1. Implement CA generation (keypair.go) - 1 day
2. Implement cert generation + SSH connection (ssh.go) - 1 day
3. Update guest setup scripts + documentation - 0.5 day
4. Testing + verification - 0.5 day

**Total: 2-3 days**

## Files to Modify

### Operator Code (~200 lines total):
1. `internal/controller/keypair.go` - Rename to CA generation (~30 lines changed)
2. `internal/controller/ssh.go` - Add cert generation (~70 lines new)
3. `internal/controller/phases.go` - Update Secret name (~10 lines changed)
4. `cmd/main.go` - Rename function call (~5 lines changed)
5. `internal/controller/keypair_test.go` - Update tests (~20 lines changed)
6. `internal/controller/ssh_test.go` - Add cert tests (~40 lines new)

### Guest Scripts (~40 lines total):
1. `guest-helpers/linux/setup.sh` - CA trust setup (~20 lines changed)
2. `guest-helpers/windows/setup.bat` - CA trust setup (~20 lines changed)

### Documentation (~50 lines total):
1. `README.md` - Update SSH setup instructions (~30 lines)
2. `docs/CERTIFICATE_ROTATION.md` - Add SSH cert section (~20 lines)

## Testing Strategy

### Unit Tests:
- Test CA keypair generation
- Test certificate generation (valid principals, expiration)
- Test certificate signing
- Test expired certificate rejection

### Integration Tests:
- Generate cert and connect to test SSH server
- Verify cert expiration after 1 hour
- Verify principal validation
- Verify CA key rotation

### Manual Testing:
1. Deploy operator with CA
2. Run setup script on test VM
3. Create VMFR - verify cert is generated
4. SSH connection succeeds
5. Wait 1 hour - verify connection fails (cert expired)
6. Create new VMFR - new cert works

## Summary

**Complexity:** MODERATE (5-6/10) - Well-understood pattern, ~200 lines of code

**Time:** 2-3 days (implementation + testing + docs)

**Security improvement:**
- Current: Global key, no expiration, forever in VMs
- After: Per-restore certificates, 1-hour expiration, automatic cleanup

**Minimal changes needed:**
- Operator: ~200 lines across 3 files
- Guest scripts: ~40 lines (add CA trust)
- Documentation: ~50 lines

**No migration burden:** Old VMs work until rebuilt with new scripts

**Recommendation:** Implement SSH certificates - moderate effort, significant security improvement, industry best practice.
