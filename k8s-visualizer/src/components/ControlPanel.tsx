import React, { useState } from 'react';
import { ShieldAlert, RefreshCcw, Plus, Trash2, Activity, Terminal, CheckCircle, XCircle } from 'lucide-react';
import type { K8sNode, K8sPod, OperatorStatus } from '../hooks/useKubernetes';

interface ControlPanelProps {
  onLog: (msg: string, type?: 'info'|'warning'|'error'|'success'|'system'|'critical') => void;
  selectedPod: string | null;
  k8sNodes: K8sNode[];
  k8sPods: K8sPod[];
  operatorStatus: OperatorStatus | null;
  k8sApi: any; // the return value of useKubernetes
}

export function ControlPanel({ onLog, selectedPod, k8sNodes, k8sPods, operatorStatus, k8sApi }: ControlPanelProps) {
  const [newAppName, setNewAppName] = useState('');
  const [selectedNodeForDeploy, setSelectedNodeForDeploy] = useState<string>('');
  const [isProcessing, setIsProcessing] = useState(false);

  const selectedPodObj = k8sPods.find(p => p.name === selectedPod);

  const handleDeploy = () => {
    if (newAppName && k8sApi.deployCustomApp) {
      onLog(`kubectl create deployment ${newAppName} --image=nginx:alpine`, 'system');
      k8sApi.deployCustomApp(newAppName, selectedNodeForDeploy || undefined);
      setNewAppName('');
    }
  };

  const handleDelete = () => {
    if (selectedPodObj && !selectedPodObj.isCNI && !selectedPodObj.isCoreDNS) {
      onLog(`kubectl delete deployment ${selectedPodObj.appLabel} -n ${selectedPodObj.namespace}`, 'system');
      k8sApi.deleteCustomApp(selectedPodObj.appLabel);
    }
  };

  // --- EXPERIMENT 1: CNI FAILURE ---
  const exp1KillCNI = async () => {
    if (!selectedPodObj || !selectedPodObj.isCNI) return;
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.deletePod(selectedPodObj.namespace, selectedPodObj.name);
      onLog(cmd, 'system');
      onLog(`[Experiment 1] Killed CNI agent on node ${selectedPodObj.nodeName}. Watch for BGP route loss!`, 'warning');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // --- EXPERIMENT 2: CoreDNS FAILURE ---
  const exp2ScaleCoreDNS = async (replicas: number) => {
    setIsProcessing(true);
    try {
      const cmd = await k8sApi.scaleDeployment('kube-system', 'coredns', replicas);
      onLog(cmd, 'system');
      if (replicas === 0) {
        onLog(`[Experiment 2] CoreDNS scaled to 0. All cluster DNS resolution will fail!`, 'critical');
      } else {
        onLog(`[Experiment 2] Restored CoreDNS to ${replicas} replicas.`, 'success');
      }
    } catch (e: any) {
      onLog(`Failed to scale CoreDNS: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // --- EXPERIMENT 3: NetworkPolicy FAILURE ---
  const exp3ApplyDenyAll = async () => {
    if (!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS) return;
    setIsProcessing(true);
    try {
      const policy = {
        apiVersion: 'networking.k8s.io/v1',
        kind: 'NetworkPolicy',
        metadata: { name: 'deny-all-exp3', namespace: selectedPodObj.namespace },
        spec: {
          podSelector: { matchLabels: { app: selectedPodObj.appLabel } },
          policyTypes: ['Ingress', 'Egress']
        }
      };
      const cmd = await k8sApi.applyNetworkPolicy(selectedPodObj.namespace, 'deny-all-exp3', policy);
      onLog(cmd, 'system');
      onLog(`[Experiment 3] Applied Deny-All NetworkPolicy to ${selectedPodObj.appLabel}. Traffic will silently drop.`, 'critical');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  const exp3RemovePolicy = async () => {
    setIsProcessing(true);
    try {
      if (selectedPodObj) {
        const cmd = await k8sApi.deleteNetworkPolicy(selectedPodObj.namespace, 'deny-all-exp3');
        onLog(cmd, 'system');
        onLog(`[Experiment 3] Removed Deny-All NetworkPolicy. Traffic restored.`, 'success');
      }
    } catch (e: any) {
      onLog(`Note: Policy might already be deleted.`, 'info');
    }
    setIsProcessing(false);
  };

  // --- EXPERIMENT 4: POD CONNECTIVITY (iptables Drop via Node Shell) ---
  const exp4DropIptables = async () => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      // In a real environment we drop the specific pod subnet, but for safety in this demo we simulate by dropping specific port/traffic
      // Actually, we'll just log what we would run, or run the real nsenter command!
      const dropCmd = `iptables -I FORWARD -j DROP -m comment --comment "exp4-failure"`;
      const cmd = await k8sApi.runNodeShellCommand(selectedPodObj.nodeName, dropCmd);
      onLog(cmd, 'system');
      onLog(`[Experiment 4] Deployed temporary Privileged Node Shell to ${selectedPodObj.nodeName}.`, 'warning');
      onLog(`[Experiment 4] Executed iptables drop at host level. The kernel will now drop forwarded packets!`, 'critical');
    } catch (e: any) {
      onLog(`Failed to run Node Shell: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };
  
  const exp4RestoreIptables = async () => {
    if (!selectedPodObj || !selectedPodObj.nodeName) return;
    setIsProcessing(true);
    try {
      const restoreCmd = `iptables -D FORWARD -j DROP -m comment --comment "exp4-failure"`;
      const cmd = await k8sApi.runNodeShellCommand(selectedPodObj.nodeName, restoreCmd);
      onLog(cmd, 'system');
      onLog(`[Experiment 4] Restored iptables rules on ${selectedPodObj.nodeName}.`, 'success');
    } catch (e: any) {
      onLog(`Failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  // Utility to test ping
  const runPingTest = async () => {
    if (!selectedPodObj) return;
    setIsProcessing(true);
    try {
      onLog(`kubectl exec -it ${selectedPodObj.name} -- ping -c 3 8.8.8.8`, 'system');
      onLog(`Running real ping test... please wait 3-4 seconds.`, 'info');
      // For simplicity, we just use our job wrapper to run a ping
      const output = await k8sApi.runCommandAndGetLogs(`ping -c 3 8.8.8.8`, selectedPodObj.appLabel);
      onLog(`Ping Output:\n${output}`, 'info');
    } catch(e: any) {
      onLog(`Ping failed: ${e.message}`, 'error');
    }
    setIsProcessing(false);
  };

  return (
    <div className="control-panel glass-panel" style={{ overflowY: 'auto' }}>
      <div className="panel-header">
        <Terminal className="icon" size={24} color="#3b82f6" />
        <h2>Real Experiments</h2>
      </div>

      <div style={{ display: 'flex', gap: '8px', marginBottom: '15px', flexDirection: 'column' }}>
        <input 
          type="text" 
          placeholder="App name (e.g. frontend)" 
          value={newAppName}
          onChange={(e) => setNewAppName(e.target.value)}
          className="app-input"
        />
        <button className="btn deploy-btn" onClick={handleDeploy} disabled={!newAppName || isProcessing}>
          <Plus size={18} />
          Deploy Standard App
        </button>
      </div>

      <div className="selected-pod-banner" style={{ borderLeft: '4px solid #3b82f6' }}>
        <strong>Target Selected:</strong> {selectedPod || 'None (Click a Pod!)'}
      </div>

      {/* EXPERIMENTS UI */}
      <div className="experiments-section" style={{ display: 'flex', flexDirection: 'column', gap: '15px', marginTop: '15px' }}>
        
        {/* Exp 1 */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4>1. CNI Failure</h4>
          <p style={{fontSize: '0.8rem', color: '#aaa', margin: '4px 0'}}>Select a `calico-node` pod to test CNI disruption.</p>
          <button 
            className="btn crash-btn" 
            onClick={exp1KillCNI} 
            disabled={!selectedPodObj?.isCNI || isProcessing}
          >
            <ShieldAlert size={16} /> Kill Calico Pod
          </button>
        </div>

        {/* Exp 2 */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4>2. CoreDNS Failure</h4>
          <p style={{fontSize: '0.8rem', color: '#aaa', margin: '4px 0'}}>Scale CoreDNS to test service discovery failure.</p>
          <div style={{ display: 'flex', gap: '8px' }}>
            <button className="btn crash-btn" onClick={() => exp2ScaleCoreDNS(0)} disabled={isProcessing}>
              Scale to 0
            </button>
            <button className="btn heal-btn" onClick={() => exp2ScaleCoreDNS(1)} disabled={isProcessing}>
              Restore to 1
            </button>
          </div>
        </div>

        {/* Exp 3 */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4>3. NetworkPolicy Failure</h4>
          <p style={{fontSize: '0.8rem', color: '#aaa', margin: '4px 0'}}>Select an app pod to enforce Deny-All policy.</p>
          <div style={{ display: 'flex', gap: '8px' }}>
            <button className="btn crash-btn" onClick={exp3ApplyDenyAll} disabled={!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS || isProcessing}>
              Deny All
            </button>
            <button className="btn heal-btn" onClick={exp3RemovePolicy} disabled={!selectedPodObj || isProcessing}>
              Remove
            </button>
          </div>
        </div>

        {/* Exp 4 */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4>4. Host Connectivity (Node-Shell)</h4>
          <p style={{fontSize: '0.8rem', color: '#aaa', margin: '4px 0'}}>Select any pod to drop IPTables on its host node natively.</p>
          <div style={{ display: 'flex', gap: '8px' }}>
            <button className="btn crash-btn" onClick={exp4DropIptables} disabled={!selectedPodObj || isProcessing}>
              Drop IP-Tables
            </button>
            <button className="btn heal-btn" onClick={exp4RestoreIptables} disabled={!selectedPodObj || isProcessing}>
              Restore
            </button>
          </div>
        </div>
        
        {/* Tools */}
        <div className="experiment-card" style={{ padding: '10px', background: 'rgba(255,255,255,0.05)', borderRadius: '8px' }}>
          <h4>Verification Tools</h4>
          <div style={{ display: 'flex', gap: '8px' }}>
            <button className="btn" onClick={runPingTest} disabled={!selectedPodObj || isProcessing} style={{ background: '#4b5563', color: 'white' }}>
              Run Ping Test (via Job)
            </button>
            <button className="btn" onClick={handleDelete} disabled={!selectedPodObj || selectedPodObj.isCNI || selectedPodObj.isCoreDNS || isProcessing} style={{ background: '#ef4444', color: 'white' }}>
              <Trash2 size={16} /> Delete App
            </button>
          </div>
        </div>

      </div>

      {/* OPERATOR STATUS */}
      <div className="status-box" style={{ marginTop: '20px' }}>
        <h3>Operator Remediation Status</h3>
        {!operatorStatus ? (
          <div style={{ fontSize: '0.85rem', color: '#888' }}>Waiting for CRD (networkremediation-sample)...</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', marginTop: '10px' }}>
             <StatusRow name="CNI Module" status={operatorStatus.cni} />
             <StatusRow name="CoreDNS Module" status={operatorStatus.coreDNS} />
             <StatusRow name="NetPolicy Module" status={operatorStatus.networkPolicy} />
             <StatusRow name="Connectivity Module" status={operatorStatus.podConnectivity} />
          </div>
        )}
      </div>
    </div>
  );
}

function StatusRow({ name, status }: { name: string, status: { healthy: boolean, enabled: boolean, message?: string } }) {
  if (!status.enabled) return <div style={{ fontSize: '0.85rem', color: '#666', display: 'flex', alignItems: 'center' }}><span style={{width: '16px'}}></span> {name}: Disabled</div>;
  
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', fontSize: '0.85rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
        {status.healthy ? <CheckCircle size={14} color="#10b981" /> : <XCircle size={14} color="#ef4444" className="pulse-red" />}
        <span style={{ color: status.healthy ? '#d1d5db' : '#ef4444' }}>{name}</span>
      </div>
      <div style={{ fontSize: '0.75rem', color: '#888', maxWidth: '100px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {status.message || (status.healthy ? 'Healthy' : 'Degraded')}
      </div>
    </div>
  );
}

