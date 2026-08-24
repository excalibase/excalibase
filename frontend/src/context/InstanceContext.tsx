import { createContext, useContext, useState, useEffect, useMemo } from 'react';
import { useInstances } from '../hooks/useProvisioning';

interface InstanceContextType {
  readonly projectId: string;
  readonly setProjectId: (id: string) => void;
}

const InstanceContext = createContext<InstanceContextType>({
  projectId: '',
  setProjectId: () => {},
});

export const useInstanceContext = () => useContext(InstanceContext);

export function InstanceProvider({ children }: { readonly children: React.ReactNode }) {
  const [projectId, setProjectId] = useState('');
  const { data: instances = [] } = useInstances();

  // Auto-select the first instance once loaded
  useEffect(() => {
    if (!projectId && instances.length > 0) {
      setProjectId(instances[0].projectId);
    }
  }, [instances, projectId]);

  const value = useMemo<InstanceContextType>(
    () => ({ projectId, setProjectId }),
    [projectId],
  );

  return (
    <InstanceContext.Provider value={value}>
      {children}
    </InstanceContext.Provider>
  );
}
