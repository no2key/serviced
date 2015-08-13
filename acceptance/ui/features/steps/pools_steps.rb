Given (/^(?:|that )multiple resource pools have been added$/) do
    visitPoolsPage()
    if @pools_page.pool_entries.size < 4
        removeAllPools()
        addDefaultPool()
        addPoolJson("pool2")
        addPoolJson("pool3")
        addPoolJson("pool4")
        expect(isInRows("table://pools/pool2/name")).to be true
        expect(isInRows("table://pools/pool4/name")).to be true
    end
end

Given (/^(?:|that )the default resource pool is added$/) do
    visitPoolsPage()
    hasDefault = isInRows("default")
    if (hasDefault == false)
        addDefaultPool()
    end
end

Given (/^(?:|that )only the default resource pool is added$/) do
    visitPoolsPage()
    if (page.has_no_content?("Showing 1 Result") || isNotInRows("default"))
        removeAllPools()
        addDefaultPool()
    end
end

Given (/^(?:|that )the "(.*?)" pool is added$/) do |pool|
    visitPoolsPage()
    if (isNotInRows(pool))
        addPool(pool, "added for tests")
    end
end

Given (/^(?:|that )the "(.*?)" virtual IP is added to the "(.*?)" pool$/) do |ip, pool|
    visitPoolsPage()
    if (isNotInRows(pool))
        addPool(pool, "added for virtual IP")
    end
    viewDetails(pool, "pools")
    if (isNotInRows("table://virtualips/" + ip + "/ip"))
        addVirtualIpJson(ip)
    end
end

Given (/^(?:|that )the "(.*?)" pool has no virtual IPs$/) do |pool|
    visitPoolsPage()
    if (isNotInRows(pool))
        addPool(pool, "added for no virtual IPs")
    else
        viewDetails(pool, "pools")
        if (@pools_page.virtualIps_table.has_no_text?("No Data Found"))
            removeAllEntries("address")
        end
    end
end

When (/^I am on the resource pool page$/) do
    visitPoolsPage()
end

When (/^I click the add Resource Pool button$/) do
    clickAddPoolButton()
end

When (/^I fill in the Resource Pool name field with "(.*?)"$/) do |resourcePool|
    fillInResourcePoolField(resourcePool)
end

When (/^I fill in the Description field with "(.*?)"$/) do |description|
    fillInDescriptionField(description)
end

When (/^I add the "(.*?)" pool$/) do |pool|
    addPoolJson(pool)
end

When (/^I click the Add Virtual IP button$/) do
    clickAddVirtualIpButton()
end

When (/^I add the virtual IP$/) do
    addVirtualIpButton()
end

When (/^I fill in the IP field with "(.*?)"$/) do |ip|
    fillInIpField(ip)
end

When (/^I fill in the Netmask field with "(.*?)"$/) do |netmask|
    fillInNetmaskField(netmask)
end

When (/^I fill in the Interface field with "(.*?)"$/) do |interface|
    fillInInterfaceField(interface)
end

Then (/^I should see the add Resource Pool button$/) do
    @pools_page.addPool_button.visible?
end

Then (/^I should see the Resource Pool name field$/) do
    @pools_page.poolName_input.visible?
end

Then (/^I should see the Description field$/) do
    @pools_page.description_input.visible?
end

Then (/^I should see the IP field$/) do
    @pools_page.ip_input.visible?
end

Then (/^I should see the Netmask field$/) do
    @pools_page.netmask_input.visible?
end

Then (/^I should see the Interface field$/) do
    @pools_page.interface_input.visible?
end

def visitPoolsPage()
    @pools_page = Pools.new
    @pools_page.navbar.resourcePools.click()
    expect(@pools_page).to be_displayed
end

def clickAddPoolButton()
    @pools_page.addPool_button.click()
end

def fillInResourcePoolField(name)
    @pools_page.poolName_input.set getTableValue(name)
end

def fillInDescriptionField(description)
    @pools_page.description_input.set getTableValue(description)
end

def clickAddVirtualIpButton()
    @pools_page.addVirtualIp_button.click()
end

def fillInIpField(address)
    @pools_page.ip_input.set getTableValue(address)
end

def fillInNetmaskField(netmask)
    @pools_page.netmask_input.set getTableValue(netmask)
end

def fillInInterfaceField(interface)
    @pools_page.interface_input.set getTableValue(interface)
end

def addVirtualIpButton()
    @pools_page.dialogAddVirtualIp_button.click()
end

def addVirtualIp(ip, netmask, interface)
    clickAddVirtualIpButton()
    fillInIpField(ip)
    fillInNetmaskField(netmask)
    fillInInterfaceField(interface)
    addVirtualIpButton()
end

def addVirtualIpJson(ip)
    addVirtualIp("table://virtualips/" + ip + "/ip", "table://virtualips/" + ip + "/netmask",
        "table://virtualips/" + ip + "/interface")
end

def addPool(name, description)
    clickAddPoolButton()
    fillInResourcePoolField(name)
    fillInDescriptionField(description)
    click_link_or_button("Add Resource Pool")
end

def addDefaultPool()
    addPoolJson("defaultPool")
end

def addPoolJson(pool)
    addPool("table://pools/" + pool + "/name", "table://pools/" + pool + "/description")
end

def removeAllPools()
    visitHostsPage()
    removeAllEntries("host")
    visitApplicationsPage()
    removeAllEntries("service")
    visitPoolsPage()
    removeAllEntries("pool")
end