import { useQuery } from '@tanstack/react-query';
import { api } from './client';

// The PostgreSQL image catalogue, as GET /api/postgres/catalog serves it.
//
// Studio deliberately holds no list of supported majors and no rule about
// which of them can carry DocumentDB. Both live in the control plane's
// catalogue and change when it changes — a major is added, one is withdrawn,
// an image is published — and a copy here would go on offering what the
// platform no longer provisions, or refusing what it now does.
export interface PostgresMajor {
  // The bare major ("16"), exactly as a provisioning request must name it.
  major: string;
  // False while the platform has no published image for this major. Such a
  // major is real but not provisionable, so it is shown and not offered.
  available: boolean;
  // Whether this major's image carries the DocumentDB extension.
  documentDb: boolean;
  // Set exactly when documentDb is false: the sentence the customer reads
  // instead of the option. It comes from the server so it cannot disagree
  // with the catalogue that enforces it.
  documentDbUnavailableReason?: string;
}

export interface PostgresCatalog {
  documentDbRef?: string;
  majors: PostgresMajor[];
}

export const getPostgresCatalog = async (): Promise<PostgresCatalog> =>
  (await api.get<PostgresCatalog>('/postgres/catalog')).data;

export const usePostgresCatalog = () =>
  useQuery({
    queryKey: ['postgres-catalog'],
    queryFn: getPostgresCatalog,
    staleTime: 60_000,
  });

// findMajor looks one major up. It returns undefined — never a stand-in — for
// anything the catalogue does not list, so a caller has to handle "the
// platform does not offer this" rather than assume a default.
export const findMajor = (
  catalog: PostgresCatalog | undefined,
  major: string
): PostgresMajor | undefined => {
  if (!catalog || !major) return undefined;
  return catalog.majors.find((entry) => entry.major === major);
};
