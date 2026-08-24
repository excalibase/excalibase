export interface AuthUser {
  id: number;
  email: string;
  full_name: string;
  role: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
  last_login_at: string | null;
}

export interface AuthSession {
  id: number;
  user_id: number;
  email: string;
  expiry_date: string;
  created_at: string;
  revoked: boolean;
}
