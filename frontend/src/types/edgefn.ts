export interface EdgeFunction {
  id: string;
  name: string;
  code: string;
  hookType: string;
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface RuntimeStatus {
  status: string;
  healthy: boolean;
}
