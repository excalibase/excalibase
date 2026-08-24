export interface EdgeFile {
  path: string;
  content: string;
}

export interface EdgeFunction {
  id: string;
  projectId: string;
  name: string;
  description?: string;
  files: EdgeFile[];
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface RuntimeStatus {
  status: string;
  healthy: boolean;
}

export interface SecretKey {
  key: string;
}

export interface InvokeResult {
  status: number;
  headers?: Record<string, string>;
  body: string;
}

export interface LogEntry {
  level: string;
  msg: string;
  ts: number;
}
