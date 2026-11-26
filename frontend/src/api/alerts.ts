// One alert as GET /alerts/project/{id} sends it. `timestamp` is when it
// fired; an alert is either open or `resolved`.
export interface Alert {
  id: string;
  projectId: string;
  severity: 'WARNING' | 'CRITICAL';
  message: string;
  metric?: string;
  value?: number;
  threshold?: number;
  timestamp: string | null;
  resolved: boolean;
}
