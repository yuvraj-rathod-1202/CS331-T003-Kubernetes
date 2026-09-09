import React, { useState } from 'react';
import { useKubernetes } from './hooks/useKubernetes';
import { ClusterMap } from './components/ClusterMap';
import { ControlPanel } from './components/ControlPanel';
import { LogTerminal } from './components/LogTerminal';
import { PodLogsModal } from './components/PodLogsModal';
import type { LogEntry } from './components/LogTerminal';
import './index.css';

function App() {
  const k8sApi = useKubernetes();
  const { nodes, pods, error, operatorStatus } = k8sApi;
  const [selectedPod, setSelectedPod] = useState<string | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [isLogsModalOpen, setIsLogsModalOpen] = useState(false);
  
  const addLog = (msg: string, type: LogEntry['type'] = 'info') => {
    const time = new Date().toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' });
    setLogs(prev => [...prev, { timestamp: time, message: msg, type }]);
  };

  const handleSelectPod = (podName: string) => {
    setSelectedPod(podName === selectedPod ? null : podName);
  };

  return (
    <div className="app-container">
      {error && (
        <div className="error-banner">
          ⚠️ {error}
        </div>
      )}
      <ClusterMap 
        k8sNodes={nodes} 
        k8sPods={pods} 
        isCrashing={false} 
        testEdges={[]}
        pendingSource={null}
        selectedPod={selectedPod}
        onSelectPod={handleSelectPod}
      />
      <ControlPanel 
        onLog={addLog}
        selectedPod={selectedPod}
        k8sNodes={nodes}
        k8sPods={pods}
        operatorStatus={operatorStatus}
        k8sApi={k8sApi}
        onOpenLogs={() => setIsLogsModalOpen(true)}
      />
      <LogTerminal logs={logs} />
      {isLogsModalOpen && (
        <PodLogsModal 
          pod={pods.find(p => p.name === selectedPod) || null} 
          onClose={() => setIsLogsModalOpen(false)} 
          k8sApi={k8sApi} 
        />
      )}
    </div>
  );
}

export default App;

