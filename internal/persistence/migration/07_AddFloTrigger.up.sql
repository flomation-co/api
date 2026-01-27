CREATE TABLE flo_trigger (
    flo_id UUID REFERENCES flo(id) ON DELETE CASCADE ,
    trigger_id UUID REFERENCES trigger(id) ON DELETE CASCADE
);