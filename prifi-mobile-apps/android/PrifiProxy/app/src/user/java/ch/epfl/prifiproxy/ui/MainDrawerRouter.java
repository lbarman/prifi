package ch.epfl.prifiproxy.ui;

import android.content.Context;
import android.support.design.widget.NavigationView;

public class MainDrawerRouter implements DrawerRouter {
    @Override
    public boolean selected(int id, Context context) {
        return false;
    }

    public void addMenu(NavigationView navigationView) {

    }
}
