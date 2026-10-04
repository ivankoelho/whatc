import { ref } from 'vue'
import { unitsService, departmentsService, type Unit, type Department } from '@/services/api'

// The organization's units and departments, to show a contact's placement by name. Both lists are
// optional context: a user without access to them gets empty lists and the screens show "Definido"
// instead of a name. One instance per screen, so the panel reads them once for everything it shows.
export function usePlacementOptions() {
  const units = ref<Unit[]>([])
  const departments = ref<Department[]>([])

  async function load() {
    try {
      const res = await unitsService.list()
      units.value = ((res.data as any).data || res.data).units || []
    } catch { /* no access */ }
    try {
      const res = await departmentsService.list()
      departments.value = ((res.data as any).data || res.data).departments || []
    } catch { /* no access */ }
  }

  const unitName = (id: string) => units.value.find(u => u.id === id)?.name
  const departmentName = (id: string) => departments.value.find(d => d.id === id)?.name

  return { units, departments, load, unitName, departmentName }
}
