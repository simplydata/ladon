package ladon

import (
	"encoding/json"
	"sync"

	"github.com/Sirupsen/logrus"
	"github.com/go-errors/errors"
	"golang.org/x/net/context"
	r "gopkg.in/dancannon/gorethink.v2"
)

// stupid hack
type rdbSchema struct {
	ID          string          `json:"id" gorethink:"id"`
	Description string          `json:"description" gorethink:"description"`
	Subjects    []string        `json:"subjects" gorethink:"subjects"`
	Effect      string          `json:"effect" gorethink:"effect"`
	Resources   []string        `json:"resources" gorethink:"resources"`
	Actions     []string        `json:"actions" gorethink:"actions"`
	Conditions  json.RawMessage `json:"conditions" gorethink:"conditions"`
}

func rdbToPolicy(s *rdbSchema) (*DefaultPolicy, error) {
	if s == nil {
		return nil, nil
	}

	ret := &DefaultPolicy{
		ID:          s.ID,
		Description: s.Description,
		Subjects:    s.Subjects,
		Effect:      s.Effect,
		Resources:   s.Resources,
		Actions:     s.Actions,
		Conditions:  Conditions{},
	}

	if err := ret.Conditions.UnmarshalJSON(s.Conditions); err != nil {
		return nil, errors.New(err)
	}

	return ret, nil

}

func rdbFromPolicy(p Policy) (*rdbSchema, error) {
	cs, err := p.GetConditions().MarshalJSON()
	if err != nil {
		return nil, err
	}
	return &rdbSchema{
		ID:          p.GetID(),
		Description: p.GetDescription(),
		Subjects:    p.GetSubjects(),
		Effect:      p.GetEffect(),
		Resources:   p.GetResources(),
		Actions:     p.GetActions(),
		Conditions:  cs,
	}, err
}

type RethinkManager struct {
	Session *r.Session
	Table   r.Term
	sync.RWMutex

	Policies map[string]Policy
}

func (m *RethinkManager) ColdStart() error {
	m.Policies = map[string]Policy{}
	policies, err := m.Table.Run(m.Session)
	if err != nil {
		return errors.New(err)
	}

	var tbl rdbSchema
	m.Lock()
	defer m.Unlock()
	for policies.Next(&tbl) {
		policy, err := rdbToPolicy(&tbl)
		if err != nil {
			return err
		}
		m.Policies[tbl.ID] = policy
	}

	return nil
}

func (m *RethinkManager) Create(policy Policy) error {
	if err := m.publishCreate(policy); err != nil {
		return err
	}

	return nil
}

// Get retrieves a policy.
func (m *RethinkManager) Get(id string) (Policy, error) {
	m.RLock()
	defer m.RUnlock()

	p, ok := m.Policies[id]
	if !ok {
		return nil, errors.New("Not found")
	}

	return p, nil
}

// Delete removes a policy.
func (m *RethinkManager) Delete(id string) error {
	if err := m.publishDelete(id); err != nil {
		return err
	}

	return nil
}

// Finds all policies associated with the subject.
func (m *RethinkManager) FindPoliciesForSubject(subject string) (Policies, error) {
	m.RLock()
	defer m.RUnlock()

	ps := Policies{}
	for _, p := range m.Policies {
		if ok, err := Match(p, p.GetSubjects(), subject); err != nil {
			return Policies{}, err
		} else if !ok {
			continue
		}
		ps = append(ps, p)
	}
	return ps, nil
}

func (m *RethinkManager) fetch() error {
	m.Policies = map[string]Policy{}
	policies, err := m.Table.Run(m.Session)
	if err != nil {
		return errors.New(err)
	}

	var policy DefaultPolicy
	m.Lock()
	defer m.Unlock()
	for policies.Next(&policy) {
		m.Policies[policy.ID] = &policy
	}

	return nil
}

func (m *RethinkManager) publishCreate(policy Policy) error {
	p, err := rdbFromPolicy(policy)
	if err != nil {
		return err
	}
	if _, err := m.Table.Insert(p).RunWrite(m.Session); err != nil {
		return errors.New(err)
	}
	return nil
}

func (m *RethinkManager) publishDelete(id string) error {
	if _, err := m.Table.Get(id).Delete().RunWrite(m.Session); err != nil {
		return errors.New(err)
	}
	return nil
}

func (m *RethinkManager) Watch(ctx context.Context) error {
	policies, err := m.Table.Changes().Run(m.Session)
	if err != nil {
		return errors.New(err)
	}

	go func() {
		for {
			var update = make(map[string]*rdbSchema)
			for policies.Next(&update) {
				newVal, err := rdbToPolicy(update["new_val"])
				if err != nil {
					logrus.Error(err)
					continue
				}

				oldVal, err := rdbToPolicy(update["old_val"])
				if err != nil {
					logrus.Error(err)
					continue
				}

				m.Lock()
				if newVal == nil && oldVal != nil {
					delete(m.Policies, oldVal.GetID())
				} else if newVal != nil && oldVal != nil {
					delete(m.Policies, oldVal.GetID())
					m.Policies[newVal.GetID()] = newVal
				} else {
					m.Policies[newVal.GetID()] = newVal
				}
				m.Unlock()
			}

			policies.Close()
			if policies.Err() != nil {
				logrus.Error(errors.New(policies.Err()))
			}

			policies, err = m.Table.Changes().Run(m.Session)
			if err != nil {
				logrus.Error(errors.New(policies.Err()))
			}
		}
	}()
	return nil
}
