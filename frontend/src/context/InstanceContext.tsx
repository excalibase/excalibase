import { createContext, useContext, useState, useEffect } from 'react';
import { useInstances } from '../hooks/useProvisioning';

interface InstanceContextType {
  projectId: string;
  setProjectId: (id: string) => void;
}

const InstanceContext = createContext<InstanceContextType>({
  projectId: '',
  setProjectId: () => {},
});

export const useInstanceContext = () => useContext(InstanceContext);

export function InstanceProvider({ children }: { children: React.ReactNode }) {
  const [projectId, setProjectId] = useState('');
  const { data: instances = [] } = useInstances();

  // Auto-select the first instance once loaded
  useEffect(() => {
    if (!projectId && instances.length > 0) {
      setProjectId(instances[0].projectId);
    }
  }, [instances, projectId]);

  return (
    <InstanceContext.Provider value={{ projectId, setProjectId }}>
      {children}
    </InstanceContext.Provider>
  );
}
