package main

import "testing"

// The page compares the fingerprint pasted from the node with the one received from the cluster (D163).
// The server repeats the comparison; this only checks what the page shows.
func TestTLSFingerprintCompare(t *testing.T) {
	runSettingsJS(t, `const T={`+settingsJS(t, "/* pmgr:tlsfp:begin */", "/* pmgr:tlsfp:end */")+`};
const assert=require('node:assert/strict');
const fp='SHA256:'+'A'.repeat(43), other='SHA256:'+'B'.repeat(43);
assert.equal(T.fingerprintOf('root@n1:~# openssl ...\n'+fp+'\nroot@n1:~# '),fp);
assert.equal(T.fingerprintOf('nothing here'),'');
assert.equal(T.fingerprintOf(null),'');
assert.equal(T.fingerprintOf('SHA256:short'),'');
assert.equal(T.compare(fp,fp),'match');
assert.equal(T.compare(fp,'  '+fp+'  \n'),'match');
assert.equal(T.compare(fp,other),'mismatch');
assert.equal(T.compare(fp,''),'empty');
assert.equal(T.compare(fp,'<img src=x onerror=alert(1)>'),'empty');
`)
}
