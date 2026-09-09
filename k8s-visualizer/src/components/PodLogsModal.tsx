import React, { useEffect, useRef, useState } from 'react';
import { X, RefreshCw, TerminalSquare } from 'lucide-react';
import type { K8sPod } from '../hooks/useKubernetes';

interface PodLogsModalProps {
  pod: K8sPod | null;
  onClose: () => void;
  k8sApi: any;
}

export function PodLogsModal({ pod, onClose, k8sApi }: PodLogsModalProps) {
  const [logs, setLogs] = useState<string>('Fetching logs...');
  const [loading, setLoading] = useState(false);
  const scrollRef = useRef<HTMLPreElement>(null);

  const fetchLogs = async () => {
    if (!pod) return;
    setLoading(true);
    try {
      const text = await k8sApi.getPodLogs(pod.namespace, pod.name);
      setLogs(text || '(No logs available)');
    } catch (e: any) {
      setLogs(`Error fetching logs: ${e.message}`);
    }
    setLoading(false);
  };

  useEffect(() => {
    if (pod) {
      fetchLogs();
    }
  }, [pod]);

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [logs]);

  if (!pod) return null;

  return (
    <div className="modal-overlay" style={{
      position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
      backgroundColor: 'rgba(0,0,0,0.6)',
      backdropFilter: 'blur(4px)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      zIndex: 9999
    }}>
      <div className="modal-content glass-panel" style={{
        width: '80%', maxWidth: '900px', height: '70vh',
        display: 'flex', flexDirection: 'column',
        boxShadow: '0 25px 50px -12px rgba(0, 0, 0, 0.5)'
      }}>
        <div className="panel-header" style={{ justifyContent: 'space-between', padding: '12px 20px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
            <TerminalSquare size={20} color="#3b82f6" />
            <h2 style={{ margin: 0, fontSize: '1.1rem' }}>Logs: {pod.name}</h2>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: '15px' }}>
            <button 
              className="btn heal-btn" 
              onClick={fetchLogs} 
              disabled={loading}
              style={{ padding: '6px 12px', fontSize: '0.8rem', width: 'auto' }}
            >
              <RefreshCw size={14} className={loading ? 'spin' : ''} /> Refresh
            </button>
            <button 
              onClick={onClose} 
              style={{ background: 'transparent', border: 'none', color: '#cbd5e1', cursor: 'pointer' }}
            >
              <X size={24} />
            </button>
          </div>
        </div>
        
        <pre ref={scrollRef} style={{
          flex: 1,
          margin: 0,
          padding: '20px',
          background: '#0f172a',
          color: '#e2e8f0',
          fontFamily: 'monospace',
          fontSize: '0.85rem',
          overflowY: 'auto',
          whiteSpace: 'pre-wrap',
          wordWrap: 'break-word',
          borderBottomLeftRadius: '12px',
          borderBottomRightRadius: '12px'
        }}>
          {logs}
        </pre>
      </div>
    </div>
  );
}
