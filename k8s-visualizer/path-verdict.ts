export type PathState = 'working' | 'broken' | 'unknown';

export interface NodePath {
  state: PathState;
  label: string;
}

// links[a][b] is the result of a real request from the probe pod on node a to the
// one on node b: true = worked, false = failed, missing = could not be measured.
export type Links = Record<string, Record<string, boolean | undefined>>;

// Works out which node is to blame from the pairs that failed.
// A node is blamed only if all its pairs failed while some other pair worked.
// If every pair failed there is no working pair to compare with, so the result
// says it cannot tell. Two nodes alone can never separate node from link.
export function nodeVerdicts(nodes: string[], links: Links): Record<string, NodePath> {
  const pairOk = (a: string, b: string): boolean | undefined => {
    const forward = links[a]?.[b];
    const back = links[b]?.[a];
    if (forward === undefined || back === undefined) return undefined;
    return forward && back;
  };

  const peersOf = (node: string) => nodes.filter(n => n !== node && pairOk(node, n) !== undefined);
  const failedPeersOf = (node: string) => peersOf(node).filter(n => pairOk(node, n) === false);
  const anyPairWorked = nodes.some(a => nodes.some(b => a < b && pairOk(a, b) === true));

  const brokenNodes = new Set(nodes.filter(n =>
    anyPairWorked && peersOf(n).length > 0 && failedPeersOf(n).length === peersOf(n).length));

  return Object.fromEntries(nodes.map(node => {
    const peers = peersOf(node);
    const failed = failedPeersOf(node);
    if (peers.length === 0) return [node, { state: 'unknown', label: 'Not measured (no probe result)' }];
    if (brokenNodes.has(node)) {
      return [node, { state: 'broken', label: `Broken (no reply from ${failed.length} of ${peers.length} nodes)` }];
    }
    const unexplained = failed.filter(peer => !brokenNodes.has(peer));
    if (unexplained.length === 0) {
      return [node, { state: 'working', label: `Working (reaches ${peers.length - failed.length} of ${peers.length} nodes)` }];
    }
    if (!anyPairWorked) return [node, { state: 'broken', label: 'Broken (cannot tell which node)' }];
    return [node, { state: 'broken', label: `Link broken to ${unexplained.join(', ')}` }];
  }));
}
